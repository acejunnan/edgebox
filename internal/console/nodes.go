package console

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"edgebox/internal/store"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

type Node struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Transport   string `json:"transport"`
	Description string `json:"description"`
}
type Nodes struct {
	Schema            int    `json:"schema"`
	Revision          uint64 `json:"revision"`
	Preferred         string `json:"preferred"`
	Profiles          []Node `json:"profiles"`
	Active            string `json:"active"`
	OperationalStatus string `json:"operational_status"`
}

func (n Nodes) Validate() error {
	if n.Schema != 1 || len(n.Profiles) > 16 {
		return errors.New("unsupported schema or too many profiles")
	}
	seen := map[string]bool{}
	for _, p := range n.Profiles {
		u, err := url.Parse(p.Endpoint)
		if !identifier.MatchString(p.ID) || seen[p.ID] || !identifier.MatchString(p.Transport) || p.Name == "" || len(p.Name) > 80 || len(p.Description) > 512 || err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme == "" || u.Scheme == "file" || u.Scheme == "javascript" || u.Scheme == "data" || strings.ContainsAny(p.Endpoint, "\r\n\x00") || len(p.Endpoint) > 256 {
			return errors.New("invalid node profile")
		}
		if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return errors.New("invalid node port")
			}
		}
		seen[p.ID] = true
	}
	if n.Preferred != "" && !seen[n.Preferred] {
		return errors.New("preferred node does not exist")
	}
	return nil
}

type NodeStore struct {
	mu    sync.Mutex
	state *store.Store
}

func OpenNodes(root string) (*NodeStore, error) {
	s, err := store.Open(root)
	if err != nil {
		return nil, err
	}
	if _, err = s.ReadCurrent(); errors.Is(err, os.ErrNotExist) {
		data, _ := json.Marshal(Nodes{Schema: 1, Profiles: []Node{}, OperationalStatus: "not_implemented"})
		err = s.CommitCurrent(data)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return &NodeStore{state: s}, nil
}
func decodeNodes(data []byte) (Nodes, error) {
	var n Nodes
	err := json.Unmarshal(data, &n)
	if err != nil {
		return n, err
	}
	if err = n.Validate(); err != nil {
		return n, err
	}
	n.Active = ""
	n.OperationalStatus = "not_implemented"
	return n, nil
}
func (s *NodeStore) Snapshot() (Nodes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.state.ReadCurrent()
	if err != nil {
		return Nodes{}, err
	}
	return decodeNodes(data)
}
func (s *NodeStore) Apply(next Nodes, expected uint64) (Nodes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apply(next, expected)
}
func (s *NodeStore) apply(next Nodes, expected uint64) (Nodes, error) {
	data, err := s.state.ReadCurrent()
	if err != nil {
		return Nodes{}, err
	}
	current, err := decodeNodes(data)
	if err != nil {
		return Nodes{}, err
	}
	if expected != current.Revision {
		return Nodes{}, errors.New("revision_conflict")
	}
	if current.Revision == ^uint64(0) {
		return Nodes{}, errors.New("revision exhausted")
	}
	if err = next.Validate(); err != nil {
		return Nodes{}, err
	}
	next.Revision = current.Revision + 1
	next.Active = ""
	next.OperationalStatus = "not_implemented"
	data, err = json.Marshal(next)
	if err != nil {
		return Nodes{}, err
	}
	if err = s.state.CommitCurrent(data); err != nil {
		return Nodes{}, err
	}
	return decodeNodes(data)
}
func (s *NodeStore) Rollback(expected uint64) (Nodes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.state.ReadPrevious()
	if err != nil {
		return Nodes{}, err
	}
	previous, err := decodeNodes(data)
	if err != nil {
		return Nodes{}, err
	}
	return s.apply(previous, expected)
}
