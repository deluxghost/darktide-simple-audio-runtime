package player

import (
	"errors"
	"math"
	"unsafe"

	"darktide-simple-audio-runtime/internal/ffmpeg"
	"darktide-simple-audio-runtime/internal/xaudio"
)

const (
	bufferFrames        = 4096
	targetQueuedBuffers = 3
)

type submittedBuffer struct {
	ptr unsafe.Pointer
}

type filePlayback struct {
	path            string
	options         Options
	decoder         *ffmpeg.Decoder
	voice           *xaudio.Voice
	sampleRate      int
	channels        int
	remainingFrames int
	buffers         []submittedBuffer
	eof             bool
}

func newFilePlayback(engine *xaudio.Engine, path string, options Options) (*filePlayback, error) {
	if options.Pos < 0 {
		options.Pos = 0
	}

	channels := xaudio.StereoChannels
	if options.Spatial {
		channels = xaudio.MonoChannels
	}

	decoder, err := ffmpeg.Open(path, options.Filters, channels)
	if err != nil {
		return nil, err
	}

	sampleRate := decoder.SampleRate()
	if sampleRate <= 0 {
		decoder.Close()
		return nil, errors.New("Audio stream has no sample rate")
	}

	voice, err := engine.CreateVoice(sampleRate, channels)
	if err != nil {
		decoder.Close()
		return nil, err
	}

	activeFile := &filePlayback{
		path:       path,
		options:    options,
		decoder:    decoder,
		voice:      voice,
		sampleRate: sampleRate,
		channels:   channels,
	}

	if err := activeFile.applyOutput(); err != nil {
		activeFile.close()
		return nil, err
	}

	if options.Duration >= 0 {
		activeFile.remainingFrames = int(math.Floor(options.Duration * float64(sampleRate)))
		if activeFile.remainingFrames <= 0 {
			activeFile.close()
			return nil, errors.New("Playback range is empty")
		}
	} else {
		activeFile.remainingFrames = -1
	}

	if err := activeFile.seekStart(); err != nil {
		activeFile.close()
		return nil, err
	}

	if err := activeFile.fillQueue(); err != nil {
		activeFile.close()
		return nil, err
	}

	if activeFile.eof && len(activeFile.buffers) == 0 {
		activeFile.close()
		return nil, errors.New("Playback range is empty")
	}

	return activeFile, nil
}

func (activeFile *filePlayback) seekStart() error {
	if activeFile.options.Pos <= 0 {
		return nil
	}

	framesToDiscard := int(math.Floor(activeFile.options.Pos * float64(activeFile.sampleRate)))

	for framesToDiscard > 0 {
		frames := framesToDiscard
		if frames > bufferFrames {
			frames = bufferFrames
		}

		buffer := xaudio.Alloc(activeFile.byteCount(frames))
		if buffer == nil {
			return errors.New("Failed to allocate seek buffer")
		}

		framesRead, finished, err := activeFile.decoder.Read(buffer, frames)
		xaudio.Free(buffer)

		if err != nil {
			return err
		}

		framesToDiscard -= framesRead
		if finished && framesToDiscard > 0 {
			return errors.New("Playback range is empty")
		}
	}

	return nil
}

func (activeFile *filePlayback) close() {
	if activeFile.voice != nil {
		activeFile.voice.Destroy()
		activeFile.voice = nil
	}

	activeFile.freeSubmittedBuffers()

	if activeFile.decoder != nil {
		activeFile.decoder.Close()
		activeFile.decoder = nil
	}
}

func (activeFile *filePlayback) freeSubmittedBuffers() {
	for i := range activeFile.buffers {
		xaudio.Free(activeFile.buffers[i].ptr)
	}

	activeFile.buffers = nil
}

func (activeFile *filePlayback) reclaimSubmittedBuffers() {
	queued := activeFile.voice.Queued()
	consumed := len(activeFile.buffers) - queued

	for i := 0; i < consumed; i++ {
		xaudio.Free(activeFile.buffers[i].ptr)
		activeFile.buffers[i].ptr = nil
	}

	if consumed > 0 {
		copy(activeFile.buffers, activeFile.buffers[consumed:])
		activeFile.buffers = activeFile.buffers[:len(activeFile.buffers)-consumed]
	}
}

func (activeFile *filePlayback) restart() error {
	if activeFile.options.LoopCount == 0 {
		return errors.New("file playback has no remaining loops")
	}

	if activeFile.options.LoopCount > 0 {
		activeFile.options.LoopCount--
	}

	if activeFile.decoder != nil {
		activeFile.decoder.Close()
		activeFile.decoder = nil
	}

	decoder, err := ffmpeg.Open(activeFile.path, activeFile.options.Filters, activeFile.channels)
	if err != nil {
		return err
	}

	activeFile.decoder = decoder
	activeFile.eof = false

	if activeFile.options.Duration >= 0 {
		activeFile.remainingFrames = int(math.Floor(activeFile.options.Duration * float64(activeFile.sampleRate)))
	} else {
		activeFile.remainingFrames = -1
	}

	return activeFile.seekStart()
}

func (activeFile *filePlayback) fillQueue() error {
	for !activeFile.eof && len(activeFile.buffers) < targetQueuedBuffers {
		framesToRead := bufferFrames
		if activeFile.remainingFrames >= 0 && activeFile.remainingFrames < framesToRead {
			framesToRead = activeFile.remainingFrames
		}

		if framesToRead <= 0 {
			if err := activeFile.restartOrMarkEOF(); err != nil {
				return err
			}

			continue
		}

		buffer := xaudio.Alloc(activeFile.byteCount(bufferFrames))
		if buffer == nil {
			return errors.New("Failed to allocate audio buffer")
		}

		framesRead, finished, err := activeFile.decoder.Read(buffer, framesToRead)
		if err != nil {
			xaudio.Free(buffer)
			return err
		}

		if framesRead == 0 && !finished {
			xaudio.Free(buffer)
			return errors.New("Audio decoder made no progress")
		}

		if framesRead > 0 {
			if err := activeFile.voice.Submit(buffer, activeFile.byteCount(framesRead)); err != nil {
				xaudio.Free(buffer)
				return err
			}

			activeFile.buffers = append(activeFile.buffers, submittedBuffer{ptr: buffer})
		} else {
			xaudio.Free(buffer)
		}

		if activeFile.remainingFrames >= 0 {
			activeFile.remainingFrames -= framesRead
		}

		if finished || activeFile.remainingFrames == 0 {
			if err := activeFile.restartOrMarkEOF(); err != nil {
				return err
			}
		}
	}

	return nil
}

func (activeFile *filePlayback) restartOrMarkEOF() error {
	if activeFile.options.LoopCount != 0 {
		return activeFile.restart()
	}

	activeFile.eof = true
	return nil
}

func (activeFile *filePlayback) applyOutput() error {
	if activeFile.options.Spatial {
		return activeFile.voice.SetSpatial(activeFile.options.VolumeGain, xaudio.SpatialSettings{
			SourcePosition:   activeFile.options.SourcePosition,
			ListenerPosition: activeFile.options.ListenerPosition,
			ListenerFront:    activeFile.options.ListenerFront,
			ListenerTop:      activeFile.options.ListenerTop,
		})
	}

	return activeFile.voice.SetVolume(activeFile.options.VolumeGain)
}

func (activeFile *filePlayback) setPosition(volumeGain float64, spatialData SpatialData) error {
	activeFile.options.VolumeGain = volumeGain
	activeFile.options.Spatial = true
	activeFile.options.SpatialData = spatialData

	return activeFile.applyOutput()
}

func (activeFile *filePlayback) byteCount(frames int) int {
	return frames * activeFile.channels * 2
}
