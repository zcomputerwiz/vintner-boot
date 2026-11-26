// SPDX-License-Identifier: GPL-2.0-or-later

package status

import "testing"

func gens(gs ...Generation) []Generation { return gs }

func left(n int) *int { return &n }

func TestFellBack(t *testing.T) {
	cases := []struct {
		name     string
		booted   int
		gens     []Generation // newest first
		fell     bool
		from     int
		attempts *int
	}{
		{"spent newer", 2, gens(
			Generation{Generation: 3, State: "TRYING", TriesLeft: left(0)},
			Generation{Generation: 2, State: "GOOD"}), true, 3, left(3)},
		// The bless service marks it FAILED once it reconciles; the counter may be gone by then.
		{"marked FAILED, no counter", 2, gens(
			Generation{Generation: 3, State: "FAILED"},
			Generation{Generation: 2, State: "GOOD"}), true, 3, nil},
		{"tries left", 2, gens(
			Generation{Generation: 3, State: "TRYING", TriesLeft: left(2)},
			Generation{Generation: 2, State: "GOOD"}), false, 0, nil},
		{"newer is GOOD", 2, gens(
			Generation{Generation: 3, State: "GOOD", TriesLeft: left(0)},
			Generation{Generation: 2, State: "GOOD"}), false, 0, nil},
		// The newest failed one is named, and the used tries are its own.
		{"two failed", 1, gens(
			Generation{Generation: 3, State: "FAILED", TriesLeft: left(0)},
			Generation{Generation: 2, State: "FAILED", TriesLeft: left(1)},
			Generation{Generation: 1, State: "GOOD"}), true, 3, left(3)},
		{"failed one is older", 3, gens(
			Generation{Generation: 3, State: "TRYING", TriesLeft: left(1)},
			Generation{Generation: 2, State: "FAILED"}), false, 0, nil},
		// A newer one that is still on trial does not hide an older failure.
		{"newer trying, one failed", 1, gens(
			Generation{Generation: 3, State: "TRYING", TriesLeft: left(3)},
			Generation{Generation: 2, State: "FAILED", TriesLeft: left(0)},
			Generation{Generation: 1, State: "GOOD"}), true, 2, left(3)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gen := c.booted
			r := &Report{Tries: 3, Generations: c.gens, Booted: Booted{Kind: KindGeneration, Generation: &gen}}
			fellBack(r)
			b := r.Booted
			if b.FellBack == nil || *b.FellBack != c.fell {
				t.Fatalf("fell_back %v, want %v", b.FellBack, c.fell)
			}
			if c.fell != (b.FellBackFrom != nil) || (c.fell && *b.FellBackFrom != c.from) {
				t.Errorf("fell_back_from %v, want %v (fell %v)", b.FellBackFrom, c.from, c.fell)
			}
			if (b.FellBackAttempts == nil) != (c.attempts == nil) || (c.attempts != nil && *b.FellBackAttempts != *c.attempts) {
				t.Errorf("fell_back_attempts %v, want %v", b.FellBackAttempts, c.attempts)
			}
		})
	}
}
