package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

// The .npy files exchanged with the Python scripts are 3-D arrays (steps,
// rows, columns) of little-endian float32, or uint8 for IRENE's probability
// in percent.

var npyShape = regexp.MustCompile(`\((\d+), (\d+), (\d+)\)`)

// readNpy reads a 3-D array as fields; uint8 values are divided by 100.
func readNpy(path string) ([]*nowcast.Field, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 10 || string(b[1:6]) != "NUMPY" {
		return nil, fmt.Errorf("%s: not a .npy file", path)
	}
	hl := int(binary.LittleEndian.Uint16(b[8:10]))
	hdr := string(b[10 : 10+hl])
	m := npyShape.FindStringSubmatch(hdr)
	if m == nil {
		return nil, fmt.Errorf("%s: not a 3-D array: %q", path, hdr)
	}
	t, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	w, _ := strconv.Atoi(m[3])
	u8 := strings.Contains(hdr, "u1")
	if !u8 && !strings.Contains(hdr, "<f4") {
		return nil, fmt.Errorf("%s: unsupported dtype in %q", path, hdr)
	}
	data := b[10+hl:]
	size := 4
	if u8 {
		size = 1
	}
	if len(data) != t*w*h*size {
		return nil, fmt.Errorf("%s: %d bytes of data, want %d", path, len(data), t*w*h*size)
	}
	var out []*nowcast.Field
	for k := range t {
		f := &nowcast.Field{W: w, H: h, V: make([]float32, w*h)}
		for i := range f.V {
			if u8 {
				f.V[i] = float32(data[k*w*h+i]) / 100
			} else {
				f.V[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[4*(k*w*h+i):]))
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// writeNpy writes fields as a float32 array, through a temporary file.
func writeNpy(path string, fields []*nowcast.Field) error {
	w, h := fields[0].W, fields[0].H
	hdr := fmt.Sprintf("{'descr': '<f4', 'fortran_order': False, 'shape': (%d, %d, %d), }", len(fields), h, w)
	for (10+len(hdr)+1)%64 != 0 {
		hdr += " "
	}
	hdr += "\n"
	f, err := os.Create(path + ".part")
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	bw.WriteString("\x93NUMPY\x01\x00")
	binary.Write(bw, binary.LittleEndian, uint16(len(hdr)))
	bw.WriteString(hdr)
	buf := make([]byte, 4)
	for _, fl := range fields {
		for _, v := range fl.V {
			binary.LittleEndian.PutUint32(buf, math.Float32bits(v))
			bw.Write(buf)
		}
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(path+".part", path)
}
