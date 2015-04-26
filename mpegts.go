package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
)

const (
	MPEGTS_PACKET_SIZE = 188
	SYNC_BYTE          = 0x47
)

type SegmentBuffer struct {
	bytes.Buffer
	Pts int64
}

type MpegTSDemuxer struct {
	r   io.Reader
	cur *SegmentBuffer
}

func NewMpegTSDemuxer(src io.Reader) *MpegTSDemuxer {
	return &MpegTSDemuxer{
		r:   src,
		cur: &SegmentBuffer{},
	}
}

func (d *MpegTSDemuxer) Parse() error {

	buf := make([]byte, MPEGTS_PACKET_SIZE)

	i := 1

	for {

		_, err := io.ReadFull(d.r, buf)
		if err != nil {
			log.Println(err.Error())
			if e := d.Flush(); e != nil {
				log.Println(e.Error())
			}
			return err
		}

		header := binary.BigEndian.Uint32(buf)

		// transportError := (header & 0x800000) != 0
		payloadUnitStart := (header & 0x400000) != 0
		// transportPriority := (header & 0x200000) != 0
		pid := (header & 0x1fff00) >> 8
		// isScrambled := ((header & 0xc0) >> 8) != 0
		adaptationFieldExist := (header & 0x20) != 0
		// containsPayload := (header & 0x10) != 0
		// cc := header & 0xf

		if buf[0] != SYNC_BYTE {
			return errors.New("can't find sync byte 0x47")
		}

		// log.Printf("transportError:%v transportPriority:%v pid:%v isScrambled:%v adaptationFieldExist:%v containsPayload:%v cc:%v",
		// 	transportError, transportPriority, pid,
		// 	isScrambled, adaptationFieldExist, containsPayload, cc)

		p := buf[4:]

		if adaptationFieldExist {
			adaptationFieldLength := buf[4]

			p = p[adaptationFieldLength+1:]

			// // Set to 1 if current TS packet is in a discontinuity state
			// // with respect to either the continuity counter or the program clock reference
			// discontinuity := (buf[5] & 0x80) != 0

			// // Set to 1 if the PES packet in this
			// // TS packet starts a video/audio sequence
			// randomAccess := (buf[5] & 0x40) != 0

			// esPriority := (buf[5] & 0x20) != 0

			// // Set to 1 if adaptation field contains a PCR field
			// containsPCR := (buf[5] & 0x10) != 0

			// // Set to 1 if adaptation field contains an OPCR field
			// containsOPCR := (buf[5] & 0x08) != 0

			// // Set to 1 if adaptation field contains a splice countdown field
			// splicing := (buf[5] & 0x04) != 0

			// // Set to 1 if adaptation field contains private data bytes
			// transportPrivateData := (buf[5] & 0x02)

			// // Set to 1 if adaptation field contains an extension
			// adaptationFieldExtension := (buf[5] & 0x01)
		}

		if payloadUnitStart {
			log.Printf("parse PES pid: %d len:%d", pid, len(p))

			if pid == 256 {
				log.Printf("frame:%d", i)
				i++

				isIdr, pts, err := d.parsePes(p)
				if err != nil {
					return err
				}

				if isIdr {
					log.Printf("pts:%d prev pts:%d delta:%d",
						pts, d.cur.Pts, pts-d.cur.Pts)

					if d.cur.Pts != 0 {
						if err := d.Flush(); err != nil {
							return err
						}
					}

					d.cur.Reset()
					d.cur.Pts = pts
				}
			}
		}

		d.cur.Write(buf)
	}

	return nil
}

func (d *MpegTSDemuxer) Flush() error {
	filename := fmt.Sprintf("%d.ts", d.cur.Pts)
	err := ioutil.WriteFile(filename, d.cur.Bytes(), 0666)
	if err != nil {
		return err
	}
	return nil
}

func (d *MpegTSDemuxer) parsePes(p []byte) (bool, int64, error) {

	if !(p[0] == 0x00 && p[1] == 0x00 && p[2] == 0x01) {
		return false, -1, errors.New("error start code")
	}

	streamId := p[3]

	log.Printf("streamId:%X", streamId)

	pesPacketLength := binary.BigEndian.Uint16(p[4:6])

	log.Printf("pesPacketLength:%d", pesPacketLength)

	pesHeaderFlags := binary.BigEndian.Uint16(p[6:8])

	pesHeaderLength := int(p[8])

	log.Printf("pesHeaderLength:%d", pesHeaderLength)

	var pts, dts int64
	if (pesHeaderFlags & 0x8000) != 0 {
		pts = d.parsePesPtsDts(p[9:])
		log.Printf("pts:%d", pts)
	}

	if (pesHeaderFlags & 0x4000) != 0 {
		dts = d.parsePesPtsDts(p[14:])
		log.Printf("dts:%d", dts)
	}

	es := p[pesHeaderLength+9:]

	isIdr := parseEs(es)

	return isIdr, pts, nil
}

func parseEs(es []byte) bool {

	pos := 0
	for {
		for i := pos; i < len(es)-4; i++ {

			if es[i] == 0x00 && es[i+1] == 0x00 && es[i+2] == 0x01 {
				log.Println("Found a NAL unit with 3-byte startcode")
				pos = i + 3
				break
			}

			if es[i] == 0x00 && es[i+1] == 0x00 && es[i+2] == 0x00 && es[i+3] == 0x01 {
				log.Println("Found a NAL unit with 4-byte startcode")
				pos = i + 4
				break
			}

			if i >= len(es)-6 {
				return false
			}
		}

		// fragmentType := es[0] & 0x80
		nalType := es[pos] & 0x1F

		log.Printf("es:%X", es[pos:])

		switch nalType {
		case 5:
			log.Println("IDR (Instantaneous Decoding Refresh) Picture")
			return true
		case 6:
			log.Println("SEI (Supplemental Enhancement Information)")
		case 7:
			log.Println("SPS (Sequence Parameter Set)")
		case 8:
			log.Println("PPS (Picture Parameter Set)")
		case 9:
			log.Println("Access Unit Delimiter")
		default:
			log.Println("unknown")
		}
	}

	return false
}

func (d *MpegTSDemuxer) parsePesPtsDts(p []byte) int64 {
	res := (int64(p[0]) & 0x0e) << 29
	res |= int64(binary.BigEndian.Uint16(p[1:])>>1) << 15
	res |= int64(binary.BigEndian.Uint16(p[3:]) >> 1)
	return res
}

func main() {
	flag.Parse()
	name := flag.Arg(0)

	f, err := os.Open(name)
	if err != nil {
		log.Println(err.Error())
		return
	}
	defer f.Close()

	demux := NewMpegTSDemuxer(bufio.NewReaderSize(f, MPEGTS_PACKET_SIZE*1024))
	if err := demux.Parse(); err != nil {
		log.Println(err.Error())
	}
}
