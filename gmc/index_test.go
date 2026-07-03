package gmc

import "testing"

func TestIndexSeek(t *testing.T) {
	ix := newFileIndex()
	ix.add(1, 0, 100)
	ix.add(1, 90000, 5000)
	ix.add(1, 180000, 9000)
	ix.add(2, 0, 150)

	cases := []struct {
		pts     uint64
		wantOff int64
		wantOK  bool
	}{
		{0, 100, true},       // exact first
		{89999, 100, true},   // between -> previous
		{90000, 5000, true},  // exact middle
		{500000, 9000, true}, // beyond last -> last
	}
	for _, c := range cases {
		off, ok := ix.seek(1, c.pts)
		if ok != c.wantOK || off != c.wantOff {
			t.Fatalf("seek(1, %d) = (%d, %v), want (%d, %v)", c.pts, off, ok, c.wantOff, c.wantOK)
		}
	}
	if _, ok := ix.seek(9, 0); ok {
		t.Fatal("unknown track must return ok=false")
	}
}

func TestIndexDump(t *testing.T) {
	ix := newFileIndex()
	ix.add(2, 0, 150)
	ix.add(1, 0, 100)
	ix.add(1, 90000, 5000)
	got := ix.dump()
	want := []cpEntry{{1, 0, 100}, {1, 90000, 5000}, {2, 0, 150}}
	if len(got) != len(want) {
		t.Fatalf("len = %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dump[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
