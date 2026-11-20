package pane

import (
	"testing"
)

func TestAdvanceTable(t *testing.T) {
	cases := []struct {
		name    string
		from    State
		ch      Change
		to      State
		effects []Effect
	}{
		{"idle attach", StateIdle, ChangeAttach, StateAttaching, []Effect{EffAttach}},
		{"idle lost", StateIdle, ChangeLost, StateLostIdle, nil},
		{"idle finished", StateIdle, ChangeFinished, StateExited, nil},
		{"idle removed", StateIdle, ChangeRemoved, StateGone, nil},
		{"attaching ok", StateAttaching, ChangeAttachOK, StateAttached, nil},
		{"attaching refused", StateAttaching, ChangeAttachFail, StateIdle, nil},
		{"attaching lost keeps want", StateAttaching, ChangeLost, StateLostLive, nil},
		{"attaching close lost keeps want", StateAttaching, ChangeCloseLost, StateLostLive, nil},
		{"attaching close exited", StateAttaching, ChangeCloseExited, StateExited, nil},
		{"attaching restarted", StateAttaching, ChangeRestarted, StateAttaching, []Effect{EffReset, EffAttach}},
		{"attaching removed", StateAttaching, ChangeRemoved, StateGone, nil},
		{"attached exit", StateAttached, ChangeCloseExited, StateExited, nil},
		{"attached finished event", StateAttached, ChangeFinished, StateExited, nil},
		{"attached detach", StateAttached, ChangeCloseDetached, StateIdle, nil},
		{"attached lost keeps want", StateAttached, ChangeCloseLost, StateLostLive, nil},
		{"attached lost event", StateAttached, ChangeLost, StateLostLive, nil},
		{"attached restarted", StateAttached, ChangeRestarted, StateAttaching, []Effect{EffReset, EffAttach}},
		{"attached removed", StateAttached, ChangeRemoved, StateGone, nil},
		{"lostLive restarted reattaches", StateLostLive, ChangeRestarted, StateAttaching, []Effect{EffReset, EffAttach}},
		{"lostLive finished", StateLostLive, ChangeFinished, StateExited, nil},
		{"lostLive removed", StateLostLive, ChangeRemoved, StateGone, nil},
		{"lostLive stray detach stays", StateLostLive, ChangeCloseDetached, StateLostLive, nil},
		{"lostLive stray lost stays", StateLostLive, ChangeLost, StateLostLive, nil},
		{"lostIdle restarted", StateLostIdle, ChangeRestarted, StateIdle, nil},
		{"lostIdle user attach", StateLostIdle, ChangeAttach, StateAttaching, []Effect{EffAttach}},
		{"lostIdle finished", StateLostIdle, ChangeFinished, StateExited, nil},
		{"exited removed", StateExited, ChangeRemoved, StateGone, nil},
		{"exited created", StateExited, ChangeCreated, StateIdle, nil},
		{"exited attach attempt", StateExited, ChangeAttach, StateAttaching, []Effect{EffAttach}},
		{"gone created", StateGone, ChangeCreated, StateIdle, nil},
		{"gone attach no-op", StateGone, ChangeAttach, StateGone, nil},
		{"attached stray attach no-op", StateAttached, ChangeAttach, StateAttached, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			to, effects := advance(c.from, c.ch)
			if to != c.to {
				t.Fatalf("advance(%v, %v) = %v, want %v", c.from, c.ch, to, c.to)
			}
			if len(effects) != len(c.effects) {
				t.Fatalf("effects = %v, want %v", effects, c.effects)
			}
			for i := range effects {
				if effects[i] != c.effects[i] {
					t.Fatalf("effects = %v, want %v", effects, c.effects)
				}
			}
		})
	}
}
