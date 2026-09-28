package main

import "testing"

func sampleModel() *model {
	m := &model{rows: []row{{raw: "first\tFirst", hay: "first"}, {raw: "+new\tNew path", hay: "new path"}}}
	m.reflow()
	return m
}

func TestToggleIsExplicitAndPerInvocation(t *testing.T) {
	m := sampleModel()
	m.toggleLabel = "Bypass this start"
	if m.toggled {
		t.Fatal("new picker starts bypassed")
	}
	done, _ := m.handle(keyEvent{kind: keyCtrlB})
	if done || !m.toggled {
		t.Fatal("Ctrl-B must toggle without accepting")
	}
	m.mode = modeFilter
	m.handle(keyEvent{kind: keyRune, r: 'b'})
	if !m.toggled || m.filter != "b" {
		t.Fatal("typing a filter must not change the toggle")
	}
	m.handle(keyEvent{kind: keyCtrlB})
	if m.toggled {
		t.Fatal("second Ctrl-B must turn bypass off")
	}
	if sampleModel().toggled {
		t.Fatal("state leaked into a fresh picker")
	}
}

func TestShortcutWorksWhenActionRowIsFilteredOut(t *testing.T) {
	m := sampleModel()
	m.newRow = "+new"
	m.filter = "first"
	m.mode = modeFilter
	m.reflow()
	done, chosen := m.handle(keyEvent{kind: keyCtrlN})
	if !done || chosen == nil || chosen.raw != "+new\tNew path" {
		t.Fatal("new action inaccessible from filter")
	}
}

func TestUnconfiguredKeysDoNotChangeSelection(t *testing.T) {
	m := sampleModel()
	for _, key := range []keyKind{keyCtrlB, keyCtrlN} {
		if done, _ := m.handle(keyEvent{kind: key}); done {
			t.Fatal("new keys changed an existing caller")
		}
	}
	if m.toggled {
		t.Fatal("unconfigured toggle enabled")
	}
	done, chosen := m.handle(keyEvent{kind: keyEnter})
	if !done || chosen == nil || chosen.raw != "first\tFirst" {
		t.Fatal("ordinary selection changed")
	}
}

func TestControlKeyDecoding(t *testing.T) {
	for b, want := range map[byte]keyKind{0x02: keyCtrlB, 0x0e: keyCtrlN} {
		if got := decode(b, nil); got.kind != want {
			t.Fatalf("control byte %d decoded as %v", b, got.kind)
		}
	}
}
