package automation

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDurationJSON(t *testing.T) {
	original := Duration(15 * time.Minute)
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `"15m0s"` {
		t.Fatalf("unexpected duration JSON: %s", data)
	}
	var decoded Duration
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != original {
		t.Fatalf("decoded duration = %s, want %s", decoded.Duration(), original.Duration())
	}
}

func TestScheduleAllowsOvernightWindow(t *testing.T) {
	location := time.FixedZone("test", 8*60*60)
	schedule := Schedule{
		Weekdays: []time.Weekday{time.Monday},
		Windows:  []ActiveWindow{{Start: "22:00", End: "06:00"}},
	}
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"monday evening", time.Date(2026, 8, 31, 23, 0, 0, 0, location), true},
		{"tuesday carryover", time.Date(2026, 9, 1, 5, 59, 0, 0, location), true},
		{"tuesday after window", time.Date(2026, 9, 1, 6, 0, 0, 0, location), false},
		{"sunday evening", time.Date(2026, 8, 30, 23, 0, 0, 0, location), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := schedule.Allows(test.at); got != test.want {
				t.Fatalf("Allows(%v) = %v, want %v", test.at, got, test.want)
			}
		})
	}
}

func TestScheduleNextAllowed(t *testing.T) {
	schedule := Schedule{
		Weekdays: []time.Weekday{time.Monday},
		Windows:  []ActiveWindow{{Start: "09:00", End: "10:00"}},
	}
	start := time.Date(2026, 8, 30, 23, 30, 25, 0, time.UTC)
	want := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	if got := schedule.NextAllowed(start); !got.Equal(want) {
		t.Fatalf("NextAllowed = %v, want %v", got, want)
	}
}
