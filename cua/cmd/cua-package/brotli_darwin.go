package main

/*
#cgo LDFLAGS: -lcompression
#include <compression.h>
#include <stdint.h>

static size_t decodeBrotli(uint8_t *dst, size_t dstLen, const uint8_t *src, size_t srcLen) {
	return compression_decode_buffer(dst, dstLen, src, srcLen, NULL, COMPRESSION_BROTLI);
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

// brotliDecode decodes a raw Brotli stream of a known size with Apple's
// libcompression (macOS 12 and later).
func brotliDecode(src []byte, size int) ([]byte, error) {
	if size <= 0 || size > 256<<20 || len(src) == 0 {
		return nil, errors.New("bad Brotli resource")
	}
	// One spare byte tells a stream longer than its header apart.
	dst := make([]byte, size+1)
	n := C.decodeBrotli((*C.uint8_t)(unsafe.Pointer(&dst[0])), C.size_t(len(dst)), (*C.uint8_t)(unsafe.Pointer(&src[0])), C.size_t(len(src)))
	if int(n) != size {
		return nil, errors.New("the Brotli resource does not decode to its stated size")
	}
	return dst[:size], nil
}
