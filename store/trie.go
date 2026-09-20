package store

// trieNode represents a node in the prefix trie. Each node may hold a fact
// pointer (if a key terminates at this node) and children keyed by the next
// byte in the path. The trie supports efficient prefix scans: walk to the
// node matching the prefix, then collect all descendants.
type trieNode struct {
	// children maps the next path byte to its child node.
	children map[byte]*trieNode
	// factKey is non-empty when a stored key terminates at this node.
	factKey string
	// subtreeCount tracks the number of facts stored in this node's subtree,
	// enabling fast emptiness checks and pre-allocation.
	subtreeCount int
}

// prefixTrie is a byte-level trie for O(k) prefix lookup where k is the
// prefix length. Used by MemoryStore as a secondary index for Scan operations.
type prefixTrie struct {
	root *trieNode
}

// newPrefixTrie creates an empty trie.
func newPrefixTrie() *prefixTrie {
	return &prefixTrie{
		root: &trieNode{children: make(map[byte]*trieNode)},
	}
}

// Insert adds a key to the trie. If the key already exists, this is a no-op
// for the trie structure (the fact value lives in the map, not the trie).
func (trie *prefixTrie) Insert(key string) {
	currentNode := trie.root
	for index := 0; index < len(key); index++ {
		byteValue := key[index]
		childNode, exists := currentNode.children[byteValue]
		if !exists {
			childNode = &trieNode{children: make(map[byte]*trieNode)}
			currentNode.children[byteValue] = childNode
		}
		currentNode = childNode
	}
	if currentNode.factKey == "" {
		currentNode.factKey = key
		// Walk back up and increment subtree counts.
		trie.incrementAncestors(key)
	}
}

// Remove deletes a key from the trie. Returns true if the key was present.
func (trie *prefixTrie) Remove(key string) bool {
	currentNode := trie.root
	for index := 0; index < len(key); index++ {
		childNode, exists := currentNode.children[key[index]]
		if !exists {
			return false
		}
		currentNode = childNode
	}
	if currentNode.factKey == "" {
		return false
	}
	currentNode.factKey = ""
	trie.decrementAncestors(key)
	trie.pruneEmptyBranch(key)
	return true
}

// KeysWithPrefix returns all keys in the trie that start with the given
// prefix, in sorted order. This is the core operation: walk to the prefix
// node in O(k), then collect all descendants via DFS.
func (trie *prefixTrie) KeysWithPrefix(prefix string) []string {
	currentNode := trie.root
	for index := 0; index < len(prefix); index++ {
		childNode, exists := currentNode.children[prefix[index]]
		if !exists {
			return nil
		}
		currentNode = childNode
	}
	if currentNode.subtreeCount == 0 && currentNode.factKey == "" {
		return nil
	}
	estimatedCapacity := currentNode.subtreeCount
	if currentNode.factKey != "" {
		estimatedCapacity++
	}
	result := make([]string, 0, estimatedCapacity)
	trie.collectSorted(currentNode, &result)
	return result
}

// collectSorted performs a DFS over the subtree rooted at the given node,
// appending keys in lexicographic order (children are visited in byte order).
func (trie *prefixTrie) collectSorted(node *trieNode, result *[]string) {
	if node.factKey != "" {
		*result = append(*result, node.factKey)
	}
	if len(node.children) == 0 {
		return
	}
	// Visit children in byte order for sorted output.
	for byteValue := 0; byteValue < 256; byteValue++ {
		childNode, exists := node.children[byte(byteValue)]
		if exists {
			trie.collectSorted(childNode, result)
		}
	}
}

// incrementAncestors walks from root to the node for the given key,
// incrementing subtreeCount at each ancestor.
func (trie *prefixTrie) incrementAncestors(key string) {
	trie.root.subtreeCount++
	currentNode := trie.root
	for index := 0; index < len(key); index++ {
		currentNode = currentNode.children[key[index]]
		currentNode.subtreeCount++
	}
}

// decrementAncestors walks from root to the node for the given key,
// decrementing subtreeCount at each ancestor.
func (trie *prefixTrie) decrementAncestors(key string) {
	trie.root.subtreeCount--
	currentNode := trie.root
	for index := 0; index < len(key); index++ {
		currentNode = currentNode.children[key[index]]
		currentNode.subtreeCount--
	}
}

// pruneEmptyBranch removes childless, keyless nodes up from the leaf
// of the given key to keep memory usage proportional to stored keys.
func (trie *prefixTrie) pruneEmptyBranch(key string) {
	pathNodes := make([]*trieNode, len(key)+1)
	pathNodes[0] = trie.root
	currentNode := trie.root
	for index := 0; index < len(key); index++ {
		currentNode = currentNode.children[key[index]]
		pathNodes[index+1] = currentNode
	}
	for depth := len(key); depth > 0; depth-- {
		node := pathNodes[depth]
		if node.factKey != "" || len(node.children) > 0 {
			break
		}
		parentNode := pathNodes[depth-1]
		delete(parentNode.children, key[depth-1])
	}
}
