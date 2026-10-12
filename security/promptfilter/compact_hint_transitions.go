package promptfilter

const (
	hintTransitionByteBits = 8
	hintTransitionMaxState = ^uint64(0) >> hintTransitionByteBits
)

// 构建完失败链接后压缩单出边状态；分支状态仍用 map，根查找仍用数组。
func (a *decodedSafetyHintAutomaton) compactTransitions() {
	for index := range a.nodes {
		node := &a.nodes[index]
		if len(node.next) == 0 {
			node.next = nil
			continue
		}
		if len(node.next) != 1 {
			continue
		}
		for value, target := range node.next {
			if uint64(target) <= hintTransitionMaxState {
				node.singleTransition = uint64(target)<<hintTransitionByteBits | uint64(value)
				node.next = nil
			}
		}
	}
}

func (n *decodedSafetyHintNode) transition(value byte) (int, bool) {
	if n.singleTransition != 0 {
		return int(n.singleTransition >> hintTransitionByteBits), byte(n.singleTransition) == value
	}
	target, exists := n.next[value]
	return target, exists
}
