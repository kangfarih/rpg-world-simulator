package friends

import (
	"reflect"
	"strings"
	"testing"
)

func TestAddRemoveFlow(t *testing.T) {
	l := New("Alice")

	if !l.Add("Bob") {
		t.Fatal("Add(Bob) = false, want true")
	}
	if !l.IsFriend("bob") {
		t.Fatal("IsFriend(bob) = false after Add(Bob), want true (case-insensitive)")
	}
	if l.Add("BOB") {
		t.Fatal("Add(BOB) = true for duplicate, want false")
	}
	if got := l.Members(); !reflect.DeepEqual(got, []string{"bob"}) {
		t.Fatalf("Members = %v, want [bob]", got)
	}
	// New entries start offline with serverId -1 (TS load/add parity).
	info, ok := l.Info("Bob")
	if !ok || info.Online || info.ServerID != OfflineServerID {
		t.Fatalf("Info(Bob) = %+v,%v, want {false -1},true", info, ok)
	}

	if !l.Remove("BOB") {
		t.Fatal("Remove(BOB) = false, want true (case-insensitive)")
	}
	if l.IsFriend("bob") {
		t.Fatal("IsFriend(bob) = true after Remove, want false")
	}
	if l.Remove("bob") {
		t.Fatal("Remove(missing) = true, want false")
	}
}

func TestAddGuards(t *testing.T) {
	l := New("Alice")

	if l.Add("alice") || l.Add("ALICE") {
		t.Fatal("Add(self) = true, want false (TS FRIENDS_ADD_SELF parity)")
	}
	if l.Add(strings.Repeat("x", MaxUsernameLen+1)) {
		t.Fatal("Add(33-char) = true, want false (TS USERNAME_TOO_LONG parity)")
	}
	if !l.Add(strings.Repeat("y", MaxUsernameLen)) {
		t.Fatal("Add(32-char) = false, want true (boundary)")
	}
	if l.Add("") || l.Add("   ") {
		t.Fatal("Add(blank) = true, want false")
	}
}

func TestBlockFlow(t *testing.T) {
	l := New("Alice")

	if !l.Add("Bob") {
		t.Fatal("Add(Bob) = false, want true")
	}
	if !l.Block("BOB") {
		t.Fatal("Block(BOB) = false, want true")
	}
	if !l.IsBlocked("bob") {
		t.Fatal("IsBlocked(bob) = false after Block, want true")
	}
	// Blocking drops the friendship.
	if l.IsFriend("bob") {
		t.Fatal("IsFriend(bob) = true after Block, want false")
	}
	// A blocked name cannot be re-added (new-in-Go; no TS source).
	if l.Add("bob") {
		t.Fatal("Add(blocked) = true, want false")
	}
	if !l.Unblock("Bob") {
		t.Fatal("Unblock(Bob) = false, want true")
	}
	if l.IsBlocked("bob") {
		t.Fatal("IsBlocked(bob) = true after Unblock, want false")
	}
	if l.Unblock("bob") {
		t.Fatal("Unblock(missing) = true, want false")
	}
	// Unblock does not restore the friendship; re-add works again.
	if l.IsFriend("bob") {
		t.Fatal("IsFriend(bob) = true after Unblock without Add, want false")
	}
	if !l.Add("bob") {
		t.Fatal("Add(bob) after Unblock = false, want true")
	}
}

func TestBlockSelfAndEmpty(t *testing.T) {
	l := New("Alice")

	if l.Block("alice") {
		t.Fatal("Block(self) = true, want false")
	}
	if l.Block("") {
		t.Fatal("Block(empty) = true, want false")
	}
	if l.Block("Bob") && !l.IsBlocked("bob") {
		t.Fatal("Block(new name) should block even a non-friend")
	}
}

func TestOnlineNotifyOnlyNonBlocked(t *testing.T) {
	l := New("Alice")
	l.Add("bob")
	l.Add("carol")
	l.Add("dave")
	l.Block("carol")

	ev, ok := l.OnlineEvent("bob", 2)
	if !ok {
		t.Fatal("OnlineEvent(bob) ok = false, want true")
	}
	if ev != (OnlineNotify{Owner: "alice", Username: "bob", ServerID: 2}) {
		t.Fatalf("OnlineEvent(bob) = %+v, want {alice bob 2}", ev)
	}
	if info, _ := l.Info("bob"); !info.Online || info.ServerID != 2 {
		t.Fatalf("Info(bob) after OnlineEvent = %+v, want online on 2", info)
	}

	// Blocked names never produce events and their status is untouched.
	if _, ok := l.OnlineEvent("carol", 2); ok {
		t.Fatal("OnlineEvent(blocked carol) ok = true, want false")
	}
	if _, ok := l.OnlineEvent("mallory", 2); ok {
		t.Fatal("OnlineEvent(stranger) ok = true, want false")
	}
	if !l.ShouldNotify("dave") || l.ShouldNotify("carol") || l.ShouldNotify("mallory") {
		t.Fatal("ShouldNotify = friend:true blocked:false stranger:false, got mismatch")
	}
}

func TestSetStatusAndInactive(t *testing.T) {
	l := New("Alice")
	l.Load([]string{"Bob", "Carol"})

	if got := l.Inactive(); !reflect.DeepEqual(got, []string{"bob", "carol"}) {
		t.Fatalf("Inactive after Load = %v, want [bob carol]", got)
	}
	if !l.SetStatus("bob", true, 3) {
		t.Fatal("SetStatus(bob,online) = false, want true")
	}
	if info, _ := l.Info("bob"); !info.Online || info.ServerID != 3 {
		t.Fatalf("Info(bob) = %+v, want {true 3}", info)
	}
	if got := l.Inactive(); !reflect.DeepEqual(got, []string{"carol"}) {
		t.Fatalf("Inactive = %v, want [carol]", got)
	}
	if !l.SetStatus("bob", false, 3) {
		t.Fatal("SetStatus(bob,offline) = false, want true")
	}
	if info, _ := l.Info("bob"); info.Online || info.ServerID != OfflineServerID {
		t.Fatalf("Info(bob) after offline = %+v, want {false -1}", info)
	}
	if l.SetStatus("mallory", true, 1) {
		t.Fatal("SetStatus(stranger) = true, want false")
	}
}

func TestLoadAndSerialize(t *testing.T) {
	l := New("Alice")
	l.Load([]string{"Bob", "BOB", "Alice", "", strings.Repeat("z", 33), "carol"})

	if got := l.Serialize(); !reflect.DeepEqual(got, []string{"bob", "carol"}) {
		t.Fatalf("Serialize after Load = %v, want [bob carol] (dedup, skip self/blank/long)", got)
	}
}
