package uitest

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestParseFrame(t *testing.T) {
	body := []byte(`{"result":true}`)
	var buf bytes.Buffer
	buf.WriteString(headMarker)
	_ = binary.Write(&buf, binary.BigEndian, uint32(7))
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(body)))
	buf.Write(body)
	buf.WriteString(tailMarker)
	got, ok := parseFrame(buf.Bytes())
	if !ok || string(got) != string(body) {
		t.Fatalf("parseFrame=%q ok=%v", got, ok)
	}
	if _, ok := parseFrame(buf.Bytes()[:10]); ok {
		t.Fatal("partial frame parsed")
	}
}

func TestChooseAgent(t *testing.T) {
	asset, version, abstract := chooseAgent()
	if asset != "agent_1.2.3.so" || version != "1.2.3" || !abstract {
		t.Fatalf("chooseAgent()=(%s,%s,%v)", asset, version, abstract)
	}
}

func TestReadJPEGStream(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	_ = c1.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	frame1 := []byte{0xff, 0xd8, 1, 2, 3, 0xff, 0xd9}
	frame2 := []byte{0xff, 0xd8, 4, 5, 0xff, 0xd9}
	go func() {
		_, _ = c2.Write([]byte(`{"result":true,"pts":1}` + "\n"))
		_, _ = c2.Write(frame1)
		_, _ = c2.Write(frame2)
	}()
	var got [][]byte
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := readJPEGStream(ctx, c1, func(b []byte) { got = append(got, append([]byte(nil), b...)) })
	if err != nil && ctx.Err() == nil && err.Error() != "read pipe: i/o timeout" {
		t.Fatalf("readJPEGStream: %v", err)
	}
	if len(got) != 2 || !bytes.Equal(got[0], frame1) || !bytes.Equal(got[1], frame2) {
		t.Fatalf("frames=%v", got)
	}
}
