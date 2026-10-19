package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// pickItem is a row in a pickList.
type pickItem struct {
	Title    string
	Desc     string
	Badge    string
	Checked  bool
	Disabled bool
	// NoCheck renders this row of a multi-select list without a checkbox,
	// like a row of a single-select list.
	NoCheck bool
	// Starred marks a favourite skill.
	Starred bool
	Value   any
}

// pickList is a scrollable, filterable list with optional checkboxes.
type pickList struct {
	items    []pickItem
	visible  []int
	cursor   int
	offset   int
	multi    bool
	filter   textinput.Model
	editing  bool
	emptyMsg string
	favsOnly bool // show only starred items
}

func newPickList(items []pickItem, multi bool) *pickList {
	ti := newInput()
	ti.Prompt = "/ "
	ti.Placeholder = "filter"
	l := &pickList{multi: multi, filter: ti, emptyMsg: "nothing here"}
	l.SetItems(items)
	return l
}

// SetItems replaces the items, keeping the filter.
func (l *pickList) SetItems(items []pickItem) {
	l.items = items
	l.applyFilter()
}

func (l *pickList) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(l.filter.Value()))
	l.visible = l.visible[:0]
	for i, it := range l.items {
		if l.favsOnly && !it.Starred {
			continue
		}
		if q == "" || strings.Contains(strings.ToLower(it.Title+" "+it.Desc+" "+it.Badge), q) {
			l.visible = append(l.visible, i)
		}
	}
	if l.cursor >= len(l.visible) {
		l.cursor = max(0, len(l.visible)-1)
	}
	l.offset = 0
}

// Refilter applies the filter again after items changed in place.
// The cursor stays on its item while that is visible.
func (l *pickList) Refilter() {
	cur := -1
	if len(l.visible) > 0 {
		cur = l.visible[l.cursor]
	}
	l.applyFilter()
	if vi := slices.Index(l.visible, cur); vi >= 0 {
		l.cursor = vi
	}
}

// FavouritesOnly reports whether the list shows only starred items.
func (l *pickList) FavouritesOnly() bool { return l.favsOnly }

// SetFavouritesOnly shows only starred items, or all items again.
func (l *pickList) SetFavouritesOnly(v bool) {
	l.favsOnly = v
	l.Refilter()
}

// Current returns the item under the cursor.
func (l *pickList) Current() (*pickItem, bool) {
	if len(l.visible) == 0 {
		return nil, false
	}
	return &l.items[l.visible[l.cursor]], true
}

// Checked returns all checked items.
func (l *pickList) Checked() []pickItem {
	var out []pickItem
	for _, it := range l.items {
		if it.selected() {
			out = append(out, it)
		}
	}
	return out
}

func (it pickItem) selected() bool { return it.Checked && it.checkable() }

// checkable reports whether the item can be checked.
func (it pickItem) checkable() bool { return !it.Disabled && !it.NoCheck }

// SetVisibleChecked checks or unchecks all checkable items the filter shows.
func (l *pickList) SetVisibleChecked(v bool) {
	for _, i := range l.visible {
		if l.items[i].checkable() {
			l.items[i].Checked = v
		}
	}
}

// HiddenChecked counts the checked items the filter hides.
func (l *pickList) HiddenChecked() int {
	n := 0
	for _, it := range l.items {
		if it.selected() {
			n++
		}
	}
	for _, i := range l.visible {
		if l.items[i].selected() {
			n--
		}
	}
	return n
}

// Focus moves the cursor to the first item matching pred.
func (l *pickList) Focus(pred func(pickItem) bool) {
	for vi, i := range l.visible {
		if pred(l.items[i]) {
			l.cursor = vi
			return
		}
	}
}

// Update handles navigation keys and reports whether the key was consumed.
func (l *pickList) Update(msg tea.KeyMsg) (bool, tea.Cmd) {
	if l.editing {
		switch msg.String() {
		case "enter":
			l.editing = false
			l.filter.Blur()
			return true, nil
		case "esc":
			l.editing = false
			l.filter.Blur()
			l.filter.SetValue("")
			l.applyFilter()
			return true, nil
		case "up", "down":
			// fall through to navigation while filtering
		default:
			var cmd tea.Cmd
			l.filter, cmd = l.filter.Update(msg)
			l.applyFilter()
			return true, cmd
		}
	}
	switch msg.String() {
	case "up", "k":
		if l.cursor > 0 {
			l.cursor--
		}
	case "down", "j":
		if l.cursor < len(l.visible)-1 {
			l.cursor++
		}
	case "pgup":
		l.cursor = max(0, l.cursor-10)
	case "pgdown":
		l.cursor = min(max(0, len(l.visible)-1), l.cursor+10)
	case "home", "g":
		l.cursor = 0
	case "end", "G":
		l.cursor = max(0, len(l.visible)-1)
	case "/":
		l.editing = true
		return true, l.filter.Focus()
	case " ", "x":
		if !l.multi {
			return false, nil
		}
		if it, ok := l.Current(); ok && it.checkable() {
			it.Checked = !it.Checked
		}
	case "a":
		if !l.multi {
			return false, nil
		}
		all := true
		for _, i := range l.visible {
			if l.items[i].checkable() && !l.items[i].Checked {
				all = false
			}
		}
		l.SetVisibleChecked(!all)
	case "esc":
		if l.filter.Value() != "" {
			l.filter.SetValue("")
			l.applyFilter()
			return true, nil
		}
		return false, nil
	default:
		return false, nil
	}
	return true, nil
}

var (
	cursorStyle   = lipgloss.NewStyle().Foreground(accent).Bold(true)
	disabledStyle = lipgloss.NewStyle().Foreground(subtle).Faint(true)
	badgeStyle    = lipgloss.NewStyle().Foreground(warnColor)
	starStyle     = lipgloss.NewStyle().Foreground(warnColor)
)

// star prefixes favourite skills.
const star = "★ "

// View renders the list in at most height lines and width columns.
func (l *pickList) View(width, height int) string {
	var b strings.Builder
	if l.favsOnly {
		b.WriteString(starStyle.Render(star + "favourites only"))
		b.WriteString("\n")
		height--
	}
	if l.editing || l.filter.Value() != "" {
		b.WriteString(l.filter.View())
		b.WriteString("\n")
		height--
	}
	if len(l.visible) == 0 {
		msg := l.emptyMsg
		if l.favsOnly && len(l.items) > 0 {
			msg = "no favourites here — press f to mark one"
		}
		b.WriteString(subtleStyle.Render(msg))
		return b.String()
	}
	rowsPer := 2
	rows := max(1, height/rowsPer)
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+rows {
		l.offset = l.cursor - rows + 1
	}
	end := min(len(l.visible), l.offset+rows)
	for vi := l.offset; vi < end; vi++ {
		it := l.items[l.visible[vi]]
		prefix := "  "
		if vi == l.cursor {
			prefix = cursorStyle.Render("› ")
		}
		box := ""
		if l.multi && !it.NoCheck {
			box = "[ ] "
			if it.Checked {
				box = "[x] "
			}
		}
		title := it.Title
		switch {
		case it.Disabled:
			title = disabledStyle.Render(title)
		case vi == l.cursor:
			title = cursorStyle.Render(title)
		}
		if it.Starred {
			title = starStyle.Render(star) + title
		}
		line := prefix + box + title
		if it.Badge != "" {
			line += " " + badgeStyle.Render(it.Badge)
		}
		b.WriteString(line)
		b.WriteString("\n")
		desc := strings.Join(strings.Fields(it.Desc), " ")
		if w := width - 6; w > 10 && len([]rune(desc)) > w {
			desc = string([]rune(desc)[:w-1]) + "…"
		}
		b.WriteString("    ")
		b.WriteString(subtleStyle.Render(desc))
		b.WriteString("\n")
	}
	if len(l.visible) > rows {
		b.WriteString(subtleStyle.Render(fmt.Sprintf("  %d/%d", l.cursor+1, len(l.visible))))
	}
	return strings.TrimRight(b.String(), "\n")
}

// cursorMode is the cursor mode for text inputs; tests use a static cursor.
var cursorMode = cursor.CursorBlink

func newInput() textinput.Model {
	ti := textinput.New()
	ti.Cursor.SetMode(cursorMode)
	return ti
}
