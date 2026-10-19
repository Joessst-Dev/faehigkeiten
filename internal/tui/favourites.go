package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// toggleStar marks f, the skill of the list item it, as a favourite, or
// unmarks it when it is one. It updates the item and returns a message for
// the user.
func toggleStar(e *env, l *pickList, it *pickItem, f config.Favourite) (string, error) {
	added := false
	err := commit(e, func(cfg *config.Config) error {
		if !cfg.RemoveFavourite(f.URL, f.Path) {
			added = cfg.AddFavourite(f)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	it.Starred = added
	l.Refilter()
	if added {
		return "added " + f.Name + " to favourites", nil
	}
	return "removed " + f.Name + " from favourites", nil
}

// ---- favourite skills ----

type favouritesScreen struct {
	env  *env
	list *pickList
	msg  string
}

func newFavouritesScreen(e *env) *favouritesScreen {
	s := &favouritesScreen{env: e, list: newPickList(nil, false)}
	s.list.emptyMsg = "no favourites yet — press f on a skill to mark it"
	s.reload()
	return s
}

func (s *favouritesScreen) reload() {
	favs := s.env.app.Config.FavouriteList()
	config.SortFavourites(favs)
	items := make([]pickItem, 0, len(favs))
	for _, f := range favs {
		desc := source.DisplayName(f.URL)
		if f.Path != "." { // not the repository root
			desc += " • " + f.Path
		}
		items = append(items, pickItem{Title: f.Name, Desc: desc, Value: f})
	}
	s.list.SetItems(items)
}

func (s *favouritesScreen) Init() tea.Cmd   { return nil }
func (s *favouritesScreen) Resume() tea.Cmd { s.reload(); return nil }
func (s *favouritesScreen) Title() string   { return "Favourite skills" }
func (s *favouritesScreen) Help() string {
	return "enter open • d/f remove • / filter"
}

func (s *favouritesScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if handled, cmd := s.list.Update(k); handled {
		return cmd
	}
	switch k.String() {
	case "esc":
		return pop
	case "d", "f":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		f := it.Value.(config.Favourite)
		err := commit(s.env, func(cfg *config.Config) error {
			cfg.RemoveFavourite(f.URL, f.Path)
			return nil
		})
		if err != nil {
			s.msg = err.Error()
			return nil
		}
		s.msg = "removed " + f.Name + " from favourites"
		s.reload()
	case "enter":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		f := it.Value.(config.Favourite)
		return push(newSkillsScreen(s.env, source.DisplayName(f.URL), f.URL, f.Ref, f.Path))
	}
	return nil
}

func (s *favouritesScreen) View() string { return listWithMsg(s.env, s.list, s.msg) }
