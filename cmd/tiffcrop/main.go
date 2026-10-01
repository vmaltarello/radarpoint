// Command tiffcrop writes a horizontal band of rows of a stripped GeoTIFF,
// copying the compressed strips unchanged. Used to build testdata/ files.
//
//	tiffcrop in.tif out.tif fromRow toRow
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/vmaltarello/radarpoint/internal/raster"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: tiffcrop in.tif out.tif fromRow toRow")
		os.Exit(2)
	}
	from, err1 := strconv.Atoi(os.Args[3])
	to, err2 := strconv.Atoi(os.Args[4])
	if err1 != nil || err2 != nil {
		fmt.Fprintln(os.Stderr, "tiffcrop: rows must be integers")
		os.Exit(2)
	}
	im, err := raster.Open(os.Args[1])
	if err == nil {
		var b []byte
		if b, err = im.CropRows(from, to); err == nil {
			err = os.WriteFile(os.Args[2], b, 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tiffcrop:", err)
		os.Exit(1)
	}
}
