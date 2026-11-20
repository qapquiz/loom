// Package pane owns the emulator side of one attached terminal: the stream
// lifecycle machine, the libghostty handles, and the Screen — the ONE type
// that crosses from emulator state to the UI.
package pane

// The pane stream machine (graft 1): a total advance(state, change) function
// with states, not flags. LostLive encodes want-attach as state, so a restart
// after a loss reattaches without any boolean to keep in sync; EffReset clears
// restart rows with a fresh emulator.

// State is the pane's view of its own stream. Distinct from room.State: the
// room watches the events stream, the pane watches its stream channel.
type State uint8

const (
	StateIdle      State = iota // known, not attached
	StateAttaching              // terminal.attach in flight
	StateAttached               // stream confirmed and live (the only write phase)
	StateExited                 // program finished; exit code known
	StateLostIdle               // lost while no attach intent stood
	StateLostLive               // lost while attach intent stood: want-attach as state
	StateGone                   // removed from the core
)

func (s State) String() string {
	switch s {
	case StateAttaching:
		return "attaching"
	case StateAttached:
		return "attached"
	case StateExited:
		return "exited"
	case StateLostIdle:
		return "lost-idle"
	case StateLostLive:
		return "lost-live"
	case StateGone:
		return "gone"
	default:
		return "idle"
	}
}

// Change is what poked the machine. Close arrives in three flavors because
// hc's CLOSE payload encodes the difference: exited (program end), detached
// (our own CLOSE ack), everything else (loss closes streams with its reason).
type Change uint8

const (
	ChangeAttach Change = iota // the viewer asked to attach
	ChangeAttachOK             // attach reply confirmed the stream
	ChangeAttachFail           // attach refused (finished, not_found, unavailable, ...)
	ChangeCloseExited          // CLOSE with reason "exited"
	ChangeCloseDetached        // empty CLOSE (detach ack)
	ChangeCloseLost            // CLOSE with a loss reason (signaled, holder_lost, ...)
	ChangeLost                 // hc lost event
	ChangeRestarted            // hc restarted event
	ChangeFinished             // hc finished event
	ChangeRemoved              // hc removed event
	ChangeCreated              // hc created event: fresh incarnation after gone
)

// Effect is an action the machine wants executed; the pane runs them on the
// session goroutine.
type Effect uint8

const (
	EffAttach Effect = iota // run sess.Attach(id, pane) again
	EffReset                // fresh emulator: restart rows must not survive
)

// advance is total: any (state, change) pair maps to a next state plus
// effects. Unlisted pairs are no-ops by the default case.
func advance(st State, ch Change) (State, []Effect) {
	switch st {
	case StateIdle:
		switch ch {
		case ChangeAttach:
			return StateAttaching, []Effect{EffAttach}
		case ChangeLost:
			return StateLostIdle, nil
		case ChangeFinished:
			return StateExited, nil
		case ChangeRemoved:
			return StateGone, nil
		}
	case StateAttaching:
		switch ch {
		case ChangeAttachOK:
			return StateAttached, nil
		case ChangeAttachFail, ChangeCloseDetached:
			return StateIdle, nil
		case ChangeCloseExited, ChangeFinished:
			return StateExited, nil
		case ChangeCloseLost, ChangeLost:
			return StateLostLive, nil
		case ChangeRestarted:
			return StateAttaching, []Effect{EffReset, EffAttach}
		case ChangeRemoved:
			return StateGone, nil
		}
	case StateAttached:
		switch ch {
		case ChangeCloseExited, ChangeFinished:
			return StateExited, nil
		case ChangeCloseDetached, ChangeAttachFail:
			return StateIdle, nil
		case ChangeCloseLost, ChangeLost:
			return StateLostLive, nil
		case ChangeRestarted:
			return StateAttaching, []Effect{EffReset, EffAttach}
		case ChangeRemoved:
			return StateGone, nil
		}
	case StateExited:
		switch ch {
		case ChangeRemoved:
			return StateGone, nil
		case ChangeCreated:
			return StateIdle, nil
		}
	case StateLostIdle:
		switch ch {
		case ChangeRestarted:
			return StateIdle, nil
		case ChangeAttach:
			return StateAttaching, []Effect{EffAttach}
		case ChangeFinished:
			return StateExited, nil
		case ChangeRemoved:
			return StateGone, nil
		}
	case StateLostLive:
		// Want-attach survives the loss by being the state itself.
		switch ch {
		case ChangeRestarted:
			return StateAttaching, []Effect{EffReset, EffAttach}
		case ChangeFinished:
			return StateExited, nil
		case ChangeRemoved:
			return StateGone, nil
		}
	case StateGone:
		switch ch {
		case ChangeCreated:
			return StateIdle, nil
		}
	}
	return st, nil
}
