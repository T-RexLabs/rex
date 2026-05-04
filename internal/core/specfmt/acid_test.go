package specfmt

import "testing"

func TestParseACIDFullForm(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ref  string
		spec string
		comp string
		req  string
	}{
		{"auth-flow.AUTH.1.1", "auth-flow", "AUTH", "1.1"},
		{"sync.ORDER.3", "sync", "ORDER", "3"},
		{"execution.EXEC-SCOPE.1", "execution", "EXEC-SCOPE", "1"},
		{"execution.PERM.3-note", "execution", "PERM", "3-note"},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got, err := ParseACID(tc.ref)
			if err != nil {
				t.Fatalf("ParseACID: %v", err)
			}
			if got.Short {
				t.Fatal("Short should be false for full form")
			}
			if got.SpecID != tc.spec || got.Component != tc.comp || got.RequirementID != tc.req {
				t.Fatalf("parsed: got %+v want spec=%s comp=%s req=%s", got, tc.spec, tc.comp, tc.req)
			}
		})
	}
}

func TestParseACIDShortForm(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ref  string
		comp string
		req  string
	}{
		{"SYS.1", "SYS", "1"},
		{"EVENTS.2", "EVENTS", "2"},
		{"EXEC-SCOPE.1", "EXEC-SCOPE", "1"},
		{"PERM.3-note", "PERM", "3-note"},
		{"NAME.1.1", "NAME", "1.1"},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got, err := ParseACID(tc.ref)
			if err != nil {
				t.Fatalf("ParseACID: %v", err)
			}
			if !got.Short {
				t.Fatalf("Short should be true: %+v", got)
			}
			if got.SpecID != "" {
				t.Fatalf("SpecID should be empty: %+v", got)
			}
			if got.Component != tc.comp || got.RequirementID != tc.req {
				t.Fatalf("parsed: got %+v want comp=%s req=%s", got, tc.comp, tc.req)
			}
		})
	}
}

func TestParseACIDRejectsMalformed(t *testing.T) {
	t.Parallel()

	bad := []string{
		"",
		"justone",            // single segment
		"lower.case.1",       // mid segment "case" is not a component id
		"AUTH.1-NOTE",        // suffix must be lowercase
		"auth-flow.lower.1",  // mid segment must be COMPONENT (uppercase)
		"auth-flow.AUTH.bad", // requirement must be numeric-shaped
		"auth.1.2",           // no component segment in full form
	}
	for _, ref := range bad {
		t.Run(ref, func(t *testing.T) {
			_, err := ParseACID(ref)
			if err == nil {
				t.Fatalf("ParseACID(%q): expected error", ref)
			}
		})
	}
}

func TestParseACIDFullFormDottedRequirement(t *testing.T) {
	t.Parallel()

	// Full form: SplitN with n=3 keeps the requirement ID intact even
	// when it contains dots.
	got, err := ParseACID("overview.NAME.1.2")
	if err != nil {
		t.Fatalf("ParseACID: %v", err)
	}
	if got.SpecID != "overview" || got.Component != "NAME" || got.RequirementID != "1.2" {
		t.Fatalf("dotted requirement id lost: %+v", got)
	}
}

func TestIsComponentID(t *testing.T) {
	t.Parallel()

	good := []string{"AUTH", "RUN", "EXEC-SCOPE", "EVENTS", "A"}
	for _, s := range good {
		if !IsComponentID(s) {
			t.Errorf("IsComponentID(%q) should be true", s)
		}
	}
	bad := []string{"", "auth", "Auth", "AUTH-", "-AUTH", "A--B", "AUTH_X", "1AUTH"}
	for _, s := range bad {
		if IsComponentID(s) {
			t.Errorf("IsComponentID(%q) should be false", s)
		}
	}
}

func TestIsKebab(t *testing.T) {
	t.Parallel()

	good := []string{"auth", "auth-flow", "spec-format", "v1-rollout", "x", "ws-2"}
	for _, s := range good {
		if !IsKebab(s) {
			t.Errorf("IsKebab(%q) should be true", s)
		}
	}
	bad := []string{"", "Auth", "AUTH", "auth_flow", "auth--flow", "-auth", "auth-", "1auth"}
	for _, s := range bad {
		if IsKebab(s) {
			t.Errorf("IsKebab(%q) should be false", s)
		}
	}
}

func TestIsRequirementID(t *testing.T) {
	t.Parallel()

	good := []string{"1", "2", "10", "1.1", "1.1.1", "2-note", "1.1-note"}
	for _, s := range good {
		if !IsRequirementID(s) {
			t.Errorf("IsRequirementID(%q) should be true", s)
		}
	}
	bad := []string{"", "a", "A.1", "1.", ".1", "1-NOTE", "1-", "1--note"}
	for _, s := range bad {
		if IsRequirementID(s) {
			t.Errorf("IsRequirementID(%q) should be false", s)
		}
	}
}
