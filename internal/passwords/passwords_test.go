package passwords

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// withTestCost runs the rest of the test at bcrypt's minimum cost, for
// behaviour that doesn't depend on the cost; cost 12 itself is checked by
// TestProductionCost and TestDummy.
func withTestCost(t *testing.T) {
	oldCost, oldDummy := cost, Dummy
	t.Cleanup(func() { cost, Dummy = oldCost, oldDummy })
	UseTestCost()
}

func TestHashCut72(t *testing.T) {
	withTestCost(t)
	pw := strings.Repeat("0123456789", 10)
	h, err := Hash(pw)
	if err != nil {
		t.Fatal(err)
	}
	if !Check(pw[:72], h) || !Check(pw, h) {
		t.Error("a 100-byte password should check with its first 72 bytes and with all 100")
	}
	if Check(pw[:71], h) {
		t.Error("71 bytes accepted")
	}
}

func TestCheckRefusesOtherHashes(t *testing.T) {
	for _, h := range []string{"$1$x", ""} {
		if Check("pw", h) {
			t.Errorf("Check accepted %q", h)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	withTestCost(t)
	// the Perl conversion left $2b$15$ hashes; any cost but Cost() is rehashed
	other, err := bcrypt.GenerateFromPassword([]byte("pw"), Cost()+1)
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsRehash(string(other)) {
		t.Error("a hash at another cost should need a rehash")
	}
	fresh, err := Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	if NeedsRehash(fresh) {
		t.Error("a fresh hash should not")
	}
	if !NeedsRehash("garbage") {
		t.Error("garbage should need a rehash")
	}
}

func TestDummy(t *testing.T) {
	if Check("anything", Dummy) {
		t.Error("dummy matched")
	}
	if NeedsRehash(Dummy) {
		t.Error("dummy should be cost 12")
	}
}

func TestUseTestCost(t *testing.T) {
	withTestCost(t)
	if Cost() != bcrypt.MinCost {
		t.Fatalf("Cost() = %d, want %d", Cost(), bcrypt.MinCost)
	}
	h, err := Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := bcrypt.Cost([]byte(h)); c != bcrypt.MinCost || NeedsRehash(h) || !Check("pw", h) {
		t.Errorf("hash %q: cost %d, NeedsRehash %v", h, c, NeedsRehash(h))
	}
	if c, _ := bcrypt.Cost([]byte(Dummy)); c != bcrypt.MinCost || Check("anything", Dummy) || Check("", Dummy) {
		t.Errorf("dummy %q at cost %d, or it matched", Dummy, c)
	}
}

// Without UseTestCost, hashes are made at the production cost, and a
// hash at that cost needs no rehash.
func TestProductionCost(t *testing.T) {
	if Cost() != 12 {
		t.Fatalf("Cost() = %d, want 12", Cost())
	}
	h, err := Hash("pw")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := bcrypt.Cost([]byte(h)); c != 12 || NeedsRehash(h) || !Check("pw", h) {
		t.Errorf("hash %q: cost %d, NeedsRehash %v", h, c, NeedsRehash(h))
	}
}
