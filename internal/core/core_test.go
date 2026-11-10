package core

import (
	"slices"
	"testing"
)

func TestScopeEdits(t *testing.T) {
	st := Settings{Groups: map[string][]string{"servers": {"lychee", "hetzner"}}}
	env := &Env{Settings: st, Device: st.Device("lychee", "mac")}
	for _, c := range []struct {
		name string
		got  Scope
		want Scope
	}{
		{"include", Scope{Include: []string{"mac"}}.With("linux"), Scope{Include: []string{"mac", "linux"}}},
		{"include twice", Scope{Include: []string{"mac"}}.With("mac"), Scope{Include: []string{"mac"}}},
		{"include what was excluded", Scope{Include: []string{"all"}, Exclude: []string{"lychee"}}.With("lychee"), Scope{Include: []string{"all", "lychee"}}},
		{"exclude the last", st.Without(Scope{Include: []string{"mac"}, Exclude: []string{"lychee"}}, "mac"), Scope{}},
		{"exclude one of a group", st.Without(Scope{Include: []string{"servers"}}, "lychee"), Scope{Include: []string{"servers"}, Exclude: []string{"lychee"}}},
		{"exclude one named beside a group", st.Without(Scope{Include: []string{"mac", "lychee"}}, "lychee"), Scope{Include: []string{"mac"}, Exclude: []string{"lychee"}}},
		{"exclude one named beside devices", st.Without(Scope{Include: []string{"banana", "lychee"}}, "lychee"), Scope{Include: []string{"banana"}}},
		{"exclude a group from devices", st.Without(Scope{Include: []string{"lychee", "banana"}}, "mac"), Scope{Include: []string{"lychee", "banana"}, Exclude: []string{"mac"}}},
		{"exclude the last group", st.Without(Scope{Include: []string{"banana", "all"}, Exclude: []string{"lychee"}}, "all"), Scope{Include: []string{"banana"}, Exclude: []string{"lychee"}}},
		{"exclude from nothing", st.Without(Scope{}, "lychee"), Scope{}},
		{"added here", env.Adding(Scope{}, Scope{}), Scope{Include: []string{"lychee"}}},
		{"added for others", env.Adding(Scope{}, Scope{Include: []string{"linux"}}), Scope{Include: []string{"linux"}}},
		{"added for all but some", env.Adding(Scope{}, Scope{Include: []string{"all"}, Exclude: []string{"servers"}}), Scope{Include: []string{"all"}, Exclude: []string{"servers"}}},
		{"added for a device but not its group", env.Adding(Scope{}, Scope{Include: []string{"lychee"}, Exclude: []string{"mac"}}), Scope{Include: []string{"lychee"}, Exclude: []string{"mac"}}},
		{"added again here", env.Adding(Scope{Include: []string{"mac"}}, Scope{}), Scope{Include: []string{"mac"}}},
		{"added again from elsewhere", env.Adding(Scope{Include: []string{"linux"}}, Scope{}), Scope{Include: []string{"linux", "lychee"}}},
		{"added again for others", env.Adding(Scope{Include: []string{"mac"}}, Scope{Include: []string{"linux"}, Exclude: []string{"lychee"}}), Scope{Include: []string{"mac", "linux"}, Exclude: []string{"lychee"}}},
	} {
		if !slices.Equal(c.got.Include, c.want.Include) || !slices.Equal(c.got.Exclude, c.want.Exclude) {
			t.Errorf("%s: %+v, want %+v", c.name, c.got, c.want)
		}
	}
}

func TestScopeCheck(t *testing.T) {
	if err := (Scope{Exclude: []string{"servers"}}).Check(); err == nil {
		t.Error("an exclude with no include was let through")
	}
	for _, s := range []Scope{{}, {Include: []string{"mac"}}, {Include: []string{"all"}, Exclude: []string{"servers"}}} {
		if err := s.Check(); err != nil {
			t.Errorf("Check(%+v) = %v", s, err)
		}
	}
}

func TestDeviceIn(t *testing.T) {
	d := Device{Name: "lychee", Groups: []string{"mac", "servers"}}
	for _, c := range []struct {
		scope Scope
		want  bool
	}{
		{Scope{}, false},
		{Scope{Include: []string{"mac"}}, true},
		{Scope{Include: []string{"linux"}}, false},
		{Scope{Include: []string{"linux", "servers"}}, true},
		{Scope{Include: []string{"lychee"}}, true},
		{Scope{Include: []string{"all"}}, true},
		{Scope{Exclude: []string{"linux"}}, false},
		{Scope{Include: []string{"all"}, Exclude: []string{"lychee"}}, false},
		{Scope{Include: []string{"mac"}, Exclude: []string{"servers"}}, false},
		{Scope{Include: []string{"all"}, Exclude: []string{"linux", "hetzner"}}, true},
	} {
		if got := d.In(c.scope); got != c.want {
			t.Errorf("In(%+v) = %v, want %v", c.scope, got, c.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	for s, want := range map[*Scope]string{
		{}:                                      "no device",
		{Include: []string{"mac", "hetzner-1"}}: "mac, hetzner-1",
		{Include: []string{"all"}, Exclude: []string{"servers"}}: "all but servers",
	} {
		if got := Describe(*s); got != want {
			t.Errorf("Describe(%+v) = %q, want %q", *s, got, want)
		}
	}
}
