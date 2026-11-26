package event

import "testing"

func TestID(t *testing.T) {
	a := ID("nslkdd", "3f8a", 41)
	if len(a) != 32 {
		t.Fatalf("len(ID) = %d, want 32", len(a))
	}
	if b := ID("nslkdd", "3f8a", 41); a != b {
		t.Errorf("ID is not deterministic: %s != %s", a, b)
	}
	for _, other := range []string{
		ID("nslkdd", "3f8a", 42),
		ID("nslkdd", "3f8b", 41),
		ID("cloudtrail", "3f8a", 41),
		ID("nslkdd", "3f8a4", 1), // the separator keeps "3f8a"+"41" apart from "3f8a4"+"1"
	} {
		if other == a {
			t.Errorf("different inputs produced the same ID %s", a)
		}
	}
}

func TestHash(t *testing.T) {
	tests := []struct {
		id   string
		want uint64
	}{
		{"00000000000000ff" + "0123456789abcdef", 255},
		{"ffffffffffffffff" + "0000000000000000", 1<<64 - 1},
	}
	for _, tt := range tests {
		if got, err := Hash(tt.id); err != nil || got != tt.want {
			t.Errorf("Hash(%s) = %d, %v; want %d", tt.id, got, err, tt.want)
		}
	}
	if _, err := Hash("abc"); err == nil {
		t.Error("short ID: want an error")
	}
}
