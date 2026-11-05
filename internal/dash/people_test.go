package dash

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestResolvePeople(t *testing.T) {
	alice := Person{Login: "alice", Jira: "acct-alice", Name: "Alice"}
	bob := Person{Login: "bob", Jira: "acct-bob", Name: "Bob"}
	pair := Person{Login: "bob", Jira: "bob@example.com", Name: "Bobby"}
	for _, tc := range []struct {
		name        string
		configured  []Person
		pair        Person
		login, jira string
		want        []Person
	}{
		{name: "nobody known"},
		{name: "default is me", login: "alice",
			want: []Person{{Login: "alice", Jira: JiraCurrentUser, Me: true}}},
		{name: "default is me and the pair", pair: pair, login: "alice", jira: "acct-alice",
			want: []Person{{Login: "alice", Jira: "acct-alice", Me: true}, pair}},
		{name: "pair alone while my login is unknown", pair: pair,
			want: []Person{pair}},
		{name: "pair who is me", pair: Person{Login: "Alice", Name: "Al"}, login: "alice",
			want: []Person{{Login: "Alice", Name: "Al", Jira: JiraCurrentUser, Me: true}}},
		{name: "configured replaces the pair", configured: []Person{bob}, pair: Person{Login: "carol"}, login: "alice",
			want: []Person{{Login: "alice", Jira: JiraCurrentUser, Me: true}, bob}},
		{name: "configured me keeps its place and jira", configured: []Person{bob, alice}, login: "ALICE", jira: "other",
			want: []Person{bob, {Login: "alice", Jira: "acct-alice", Name: "Alice", Me: true}}},
		{name: "configured me without jira takes the known one", configured: []Person{{Login: "alice"}}, login: "alice", jira: "acct-alice",
			want: []Person{{Login: "alice", Jira: "acct-alice", Me: true}}},
		{name: "unknown login marks nobody", configured: []Person{alice, {Login: "bob", Me: true}},
			want: []Person{alice, {Login: "bob"}}},
		{name: "duplicates and blanks dropped", configured: []Person{alice, {Login: "Alice", Name: "dup"}, {Name: "nobody"}, bob},
			want: []Person{alice, bob}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolvePeople(tc.configured, tc.pair, tc.login, tc.jira)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestPeopleJSON pins the snapshot contract gh-100 reads: people with login,
// jira, name and me, omitted when unknown.
func TestPeopleJSON(t *testing.T) {
	b, err := json.Marshal(&Snapshot{People: []Person{{Login: "alice", Jira: "acct-alice", Name: "Alice", Me: true}, {Login: "bob"}}})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if got, want := string(doc["people"]), `[{"login":"alice","jira":"acct-alice","name":"Alice","me":true},{"login":"bob"}]`; got != want {
		t.Fatalf("people JSON %s, want %s", got, want)
	}
	b, _ = json.Marshal(&Snapshot{})
	doc = nil
	_ = json.Unmarshal(b, &doc)
	if _, ok := doc["people"]; ok {
		t.Fatal("unknown people must be omitted")
	}
}
