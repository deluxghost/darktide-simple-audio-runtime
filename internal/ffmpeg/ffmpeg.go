package ffmpeg

/*
#cgo windows CFLAGS: -DUNICODE -D_UNICODE
#include <stdlib.h>
#include "ffmpeg.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

const (
	MonoChannels   = 1
	StereoChannels = 2
)

type FileInfo struct {
	SampleRate int
	Channels   int
	Duration   float64
	BitRate    int64
	Tags       map[string]string
}

type Decoder struct {
	ptr *C.SA_Decoder
}

func errorString(buffer *C.char) string {
	if buffer == nil {
		return "unknown error"
	}

	return C.GoString(buffer)
}

func Initialize() error {
	var errbuf [512]C.char

	if C.sa_ffmpeg_initialize(&errbuf[0], C.int(len(errbuf))) == 0 {
		return errors.New(errorString(&errbuf[0]))
	}

	return nil
}

func Open(path string, filters string, outputChannels int) (*Decoder, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	var cFilters *C.char
	if filters != "" {
		cFilters = C.CString(filters)
		defer C.free(unsafe.Pointer(cFilters))
	}

	var errbuf [512]C.char
	var ptr *C.SA_Decoder

	if C.sa_decoder_open(cPath, cFilters, C.int(outputChannels), &ptr, &errbuf[0], C.int(len(errbuf))) == 0 {
		return nil, errors.New(errorString(&errbuf[0]))
	}

	return &Decoder{ptr: ptr}, nil
}

func ReadInfo(path string) (FileInfo, error) {
	decoder, err := Open(path, "", StereoChannels)
	if err != nil {
		return FileInfo{}, err
	}
	defer decoder.Close()

	return FileInfo{
		SampleRate: decoder.SampleRate(),
		Channels:   decoder.Channels(),
		Duration:   decoder.Duration(),
		BitRate:    decoder.BitRate(),
		Tags:       decoder.Tags(),
	}, nil
}

func (decoder *Decoder) Read(output unsafe.Pointer, maxFrames int) (int, bool, error) {
	if decoder == nil || decoder.ptr == nil {
		return 0, true, nil
	}

	var errbuf [512]C.char
	var framesWritten C.int
	var finished C.int

	ok := C.sa_decoder_read(
		decoder.ptr,
		(*C.int16_t)(output),
		C.int(maxFrames),
		&framesWritten,
		&finished,
		&errbuf[0],
		C.int(len(errbuf)),
	)

	if ok == 0 {
		return int(framesWritten), false, errors.New(errorString(&errbuf[0]))
	}

	return int(framesWritten), finished != 0, nil
}

func (decoder *Decoder) SampleRate() int {
	if decoder == nil || decoder.ptr == nil {
		return 0
	}

	return int(C.sa_decoder_sample_rate(decoder.ptr))
}

func (decoder *Decoder) Channels() int {
	if decoder == nil || decoder.ptr == nil {
		return 0
	}

	return int(C.sa_decoder_channels(decoder.ptr))
}

func (decoder *Decoder) BitRate() int64 {
	if decoder == nil || decoder.ptr == nil {
		return 0
	}

	return int64(C.sa_decoder_bit_rate(decoder.ptr))
}

func (decoder *Decoder) Duration() float64 {
	if decoder == nil || decoder.ptr == nil {
		return -1
	}

	return float64(C.sa_decoder_duration(decoder.ptr))
}

func (decoder *Decoder) Tags() map[string]string {
	tags := make(map[string]string)
	if decoder == nil || decoder.ptr == nil {
		return tags
	}

	for index := 0; ; index++ {
		var key *C.char
		var value *C.char

		if C.sa_decoder_tag(decoder.ptr, C.int(index), &key, &value) == 0 {
			return tags
		}

		tags[C.GoString(key)] = C.GoString(value)
	}
}

func (decoder *Decoder) Close() {
	if decoder == nil || decoder.ptr == nil {
		return
	}

	C.sa_decoder_close(decoder.ptr)
	decoder.ptr = nil
}
