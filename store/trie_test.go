package store

import "testing"

func TestTrieInsertAndKeysWithPrefix(t *testing.T) {
	trie := newPrefixTrie()
	trie.Insert("desired/service/api/image")
	trie.Insert("desired/service/web/image")
	trie.Insert("observed/instance/aaa/state")
	trie.Insert("observed/instance/bbb/state")
	trie.Insert("observed/node/node-1/state")

	result := trie.KeysWithPrefix("desired/service/")
	if len(result) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(result))
	}
	if result[0] != "desired/service/api/image" {
		t.Errorf("first key: got %s", result[0])
	}
	if result[1] != "desired/service/web/image" {
		t.Errorf("second key: got %s", result[1])
	}
}

func TestTrieKeysWithPrefixSortedOrder(t *testing.T) {
	trie := newPrefixTrie()
	trie.Insert("c/3")
	trie.Insert("a/1")
	trie.Insert("b/2")
	trie.Insert("a/2")

	result := trie.KeysWithPrefix("")
	if len(result) != 4 {
		t.Fatalf("expected 4 keys, got %d", len(result))
	}
	for index := 1; index < len(result); index++ {
		if result[index] <= result[index-1] {
			t.Errorf("not sorted: %q came after %q", result[index], result[index-1])
		}
	}
}

func TestTrieRemove(t *testing.T) {
	trie := newPrefixTrie()
	trie.Insert("desired/service/api/image")
	trie.Insert("desired/service/web/image")

	removed := trie.Remove("desired/service/api/image")
	if !removed {
		t.Fatal("expected Remove to return true for existing key")
	}

	result := trie.KeysWithPrefix("desired/service/")
	if len(result) != 1 {
		t.Fatalf("expected 1 key after removal, got %d", len(result))
	}
	if result[0] != "desired/service/web/image" {
		t.Errorf("remaining key: got %s", result[0])
	}
}

func TestTrieRemoveNonExistent(t *testing.T) {
	trie := newPrefixTrie()
	trie.Insert("desired/service/api/image")

	removed := trie.Remove("desired/service/web/image")
	if removed {
		t.Error("expected Remove to return false for non-existent key")
	}
}

func TestTrieEmptyPrefix(t *testing.T) {
	trie := newPrefixTrie()
	result := trie.KeysWithPrefix("observed/")
	if result != nil {
		t.Errorf("expected nil for empty trie prefix search, got %d keys", len(result))
	}
}

func TestTrieNoMatchingPrefix(t *testing.T) {
	trie := newPrefixTrie()
	trie.Insert("desired/service/api/image")

	result := trie.KeysWithPrefix("observed/")
	if result != nil {
		t.Errorf("expected nil for non-matching prefix, got %d keys", len(result))
	}
}

func TestTrieDuplicateInsert(t *testing.T) {
	trie := newPrefixTrie()
	trie.Insert("desired/service/api/image")
	trie.Insert("desired/service/api/image")

	result := trie.KeysWithPrefix("desired/")
	if len(result) != 1 {
		t.Fatalf("expected 1 key after duplicate insert, got %d", len(result))
	}
}

func TestTrieSubtreeCountAccurate(t *testing.T) {
	trie := newPrefixTrie()
	trie.Insert("a/1")
	trie.Insert("a/2")
	trie.Insert("a/3")
	trie.Insert("b/1")

	if trie.root.subtreeCount != 4 {
		t.Errorf("root subtreeCount: expected 4, got %d", trie.root.subtreeCount)
	}

	trie.Remove("a/2")
	if trie.root.subtreeCount != 3 {
		t.Errorf("root subtreeCount after remove: expected 3, got %d", trie.root.subtreeCount)
	}
}

func TestTriePrunesEmptyBranches(t *testing.T) {
	trie := newPrefixTrie()
	trie.Insert("deep/nested/path/key")
	trie.Remove("deep/nested/path/key")

	result := trie.KeysWithPrefix("deep/")
	if result != nil {
		t.Errorf("expected nil after removing only key, got %d keys", len(result))
	}
	if len(trie.root.children) != 0 {
		t.Errorf("expected empty root children after pruning, got %d", len(trie.root.children))
	}
}
