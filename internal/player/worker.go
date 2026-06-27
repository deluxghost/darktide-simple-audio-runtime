package player

import (
	"fmt"
	"time"

	"darktide-simple-audio-runtime/internal/xaudio"
)

const updateInterval = 5 * time.Millisecond

type commandKind int

const (
	commandPlay commandKind = iota + 1
	commandSetPosition
	commandStop
	commandStopAll
	commandShutdown
)

type command struct {
	kind        commandKind
	playID      int
	path        string
	options     Options
	volumeGain  float64
	spatialData SpatialData
	result      chan commandResult
}

type commandResult struct {
	ok    bool
	error error
}

func (player *Player) loop(engine *xaudio.Engine, commands <-chan command, events chan Event, done chan<- struct{}) {
	defer close(done)
	defer engine.Destroy()

	activeFiles := map[int]*filePlayback{}

	for {
		if len(activeFiles) == 0 {
			cmd := <-commands
			if player.handleCommand(cmd, activeFiles, engine) {
				return
			}

			continue
		}

		if player.drainCommands(commands, activeFiles, engine) {
			return
		}
		player.updateFilePlaybacks(activeFiles, events)

		select {
		case cmd := <-commands:
			if player.handleCommand(cmd, activeFiles, engine) {
				return
			}
		case <-time.After(updateInterval):
		}
	}
}

func (player *Player) drainCommands(commands <-chan command, activeFiles map[int]*filePlayback, engine *xaudio.Engine) bool {
	for {
		select {
		case cmd := <-commands:
			if player.handleCommand(cmd, activeFiles, engine) {
				return true
			}
		default:
			return false
		}
	}
}

func (player *Player) handleCommand(cmd command, activeFiles map[int]*filePlayback, engine *xaudio.Engine) bool {
	switch cmd.kind {
	case commandPlay:
		activeFile, err := newFilePlayback(engine, cmd.path, cmd.options)
		if err != nil {
			cmd.result <- commandResult{error: err}
			return false
		}

		activeFiles[cmd.playID] = activeFile
		cmd.result <- commandResult{ok: true}

	case commandSetPosition:
		activeFile := activeFiles[cmd.playID]
		if activeFile == nil {
			cmd.result <- commandResult{}
			return false
		}

		if err := activeFile.setPosition(cmd.volumeGain, cmd.spatialData); err != nil {
			cmd.result <- commandResult{error: err}
			return false
		}

		cmd.result <- commandResult{ok: true}

	case commandStop:
		activeFile := activeFiles[cmd.playID]
		if activeFile == nil {
			cmd.result <- commandResult{}
			return false
		}

		activeFile.close()
		delete(activeFiles, cmd.playID)
		player.unmarkActive(cmd.playID)
		cmd.result <- commandResult{ok: true}

	case commandStopAll:
		player.closeFilePlaybacks(activeFiles)
		player.clearActive()
		cmd.result <- commandResult{ok: true}

	case commandShutdown:
		player.closeFilePlaybacks(activeFiles)
		player.clearActive()
		cmd.result <- commandResult{ok: true}
		return true
	}

	return false
}

func (player *Player) updateFilePlaybacks(activeFiles map[int]*filePlayback, events chan Event) {
	for playID, activeFile := range activeFiles {
		activeFile.reclaimSubmittedBuffers()

		if err := activeFile.fillQueue(); err != nil {
			player.pushEvent(events, Event{Type: EventError, PlayID: playID, Message: err.Error()})
			activeFile.close()
			delete(activeFiles, playID)
			player.unmarkActive(playID)
			continue
		}

		if activeFile.eof && len(activeFile.buffers) == 0 {
			activeFile.close()
			delete(activeFiles, playID)
			player.unmarkActive(playID)
			player.pushEvent(events, Event{Type: EventFinished, PlayID: playID})
		}
	}
}

func (player *Player) closeFilePlaybacks(activeFiles map[int]*filePlayback) {
	for playID, activeFile := range activeFiles {
		activeFile.close()
		delete(activeFiles, playID)
	}
}

func (player *Player) pushEvent(events chan Event, event Event) {
	select {
	case events <- event:
	default:
		select {
		case <-events:
		default:
		}

		select {
		case events <- event:
		default:
			fmt.Println("SimpleAudio runtime event queue is full")
		}
	}
}
