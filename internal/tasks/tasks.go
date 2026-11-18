// Package tasks filters and renders tasks for the terminal.
package tasks

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/JB0t/clctl/internal/tracker"
)

var priorityRank = map[string]int{"urgent": 0, "high": 1, "normal": 2, "low": 3}

// Rank orders tasks by priority, unprioritised last.
func Rank(task tracker.Task) int {
	if rank, ok := priorityRank[task.PriorityName()]; ok {
		return rank
	}
	return len(priorityRank)
}

// Open drops tasks whose status is a done/closed status.
func Open(tasks []tracker.Task) []tracker.Task {
	var open []tracker.Task
	for _, task := range tasks {
		if !task.Status.Done() {
			open = append(open, task)
		}
	}
	return open
}

// AssignedTo keeps tasks assigned to every one of the user IDs.
func AssignedTo(tasks []tracker.Task, userIDs ...string) []tracker.Task {
	var kept []tracker.Task
	for _, task := range tasks {
		all := true
		for _, id := range userIDs {
			all = all && task.AssignedTo(id)
		}
		if all {
			kept = append(kept, task)
		}
	}
	return kept
}

// Location returns "Space/Folder/List"; lists sitting directly in a space have no folder.
func Location(task tracker.Task) string {
	parts := []string{orDefault(task.Space.Name, task.Space.ID)}
	if task.Folder.Name != "" && !task.Folder.Hidden {
		parts = append(parts, task.Folder.Name)
	}
	return strings.Join(append(parts, orDefault(task.List.Name, "?")), "/")
}

// Label is the one-line form used in pickers.
func Label(task tracker.Task) string {
	return fmt.Sprintf("%-12s %-7s %s  (%s)", task.Ref(), orDefault(task.PriorityName(), "-"), task.Name, Location(task))
}

// Sort orders tasks by priority, then most recently updated.
func Sort(tasks []tracker.Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		if Rank(tasks[i]) != Rank(tasks[j]) {
			return Rank(tasks[i]) < Rank(tasks[j])
		}
		return tasks[i].DateUpdated > tasks[j].DateUpdated
	})
}

type node struct {
	label    string
	children []node
}

// PrintTree writes tasks as a Space > Folder > List tree with subtasks nested under their
// parents and each level sorted by priority.
func PrintTree(out io.Writer, tasks []tracker.Task, color bool) {
	paint := func(text, code string) string {
		if !color || code == "" {
			return text
		}
		return "\033[" + code + "m" + text + "\033[0m"
	}
	priorityColors := map[string]string{"urgent": "31", "high": "33", "normal": "36", "low": "2"}
	taskLine := func(task tracker.Task) string {
		priority := orDefault(task.PriorityName(), "-")
		return fmt.Sprintf("%s%-11s %s  %s", paint(fmt.Sprintf("%-7s", priority), priorityColors[priority]),
			task.Ref(), task.Name, paint("["+task.Status.Status+"]", "2"))
	}

	type listKey struct{ space, folder, list string }
	groups := map[listKey][]tracker.Task{}
	for _, task := range tasks {
		folder := ""
		if !task.Folder.Hidden {
			folder = task.Folder.Name
		}
		key := listKey{orDefault(task.Space.Name, task.Space.ID), folder, orDefault(task.List.Name, "?")}
		groups[key] = append(groups[key], task)
	}

	taskNodes := func(listTasks []tracker.Task) []node {
		ids := map[string]bool{}
		for _, task := range listTasks {
			ids[task.ID] = true
		}
		children := map[string][]tracker.Task{}
		var roots []tracker.Task
		for _, task := range listTasks {
			if task.Parent != "" && ids[task.Parent] {
				children[task.Parent] = append(children[task.Parent], task)
			} else {
				roots = append(roots, task)
			}
		}
		var build func(tracker.Task) node
		build = func(task tracker.Task) node {
			kids := children[task.ID]
			Sort(kids)
			result := node{label: taskLine(task)}
			for _, kid := range kids {
				result.children = append(result.children, build(kid))
			}
			return result
		}
		Sort(roots)
		var nodes []node
		for _, task := range roots {
			nodes = append(nodes, build(task))
		}
		return nodes
	}

	spaces := map[string]map[string][]string{}
	for key := range groups {
		if spaces[key.space] == nil {
			spaces[key.space] = map[string][]string{}
		}
		spaces[key.space][key.folder] = append(spaces[key.space][key.folder], key.list)
	}
	for _, space := range sortedKeys(spaces) {
		fmt.Fprintln(out, paint(space, "1"))
		folders := spaces[space]
		var items []node
		// Lists sitting directly in the space come first, then folders alphabetically.
		for _, folder := range sortedKeys(folders) {
			lists := folders[folder]
			sort.Strings(lists)
			var listNodes []node
			for _, list := range lists {
				listTasks := groups[listKey{space, folder, list}]
				label := paint(fmt.Sprintf("%s (%d)", list, len(listTasks)), "1;34")
				listNodes = append(listNodes, node{label: label, children: taskNodes(listTasks)})
			}
			if folder == "" {
				items = append(items, listNodes...)
			} else {
				items = append(items, node{label: paint(folder, "1;35"), children: listNodes})
			}
		}
		printNodes(out, items, "")
		fmt.Fprintln(out)
	}
}

func printNodes(out io.Writer, nodes []node, prefix string) {
	for index, item := range nodes {
		last := index == len(nodes)-1
		branch, next := "├── ", "│   "
		if last {
			branch, next = "└── ", "    "
		}
		fmt.Fprintln(out, prefix+branch+item.label)
		printNodes(out, item.children, prefix+next)
	}
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys) // "" (no folder) sorts first
	return keys
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
