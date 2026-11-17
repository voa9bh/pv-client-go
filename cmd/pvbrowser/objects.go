package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type textClient interface {
	AskTxtContext(context.Context, string) (string, error)
}

type object struct {
	id   int64
	name string
	path string
	root bool
}

func rootObject() object { return object{name: "pvServer", path: "/pvServer", root: true} }

func (o object) address() string {
	if o.root {
		return "/pvServer"
	}
	return "#" + strconv.FormatInt(o.id, 10)
}

func children(ctx context.Context, client textClient, parent object, ancestors map[int64]bool) ([]object, error) {
	var result []object
	seen := make(map[int64]bool)
	previous := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		command := fmt.Sprintf("%s.getNextChildObject(%d)", parent.address(), previous)
		reply, err := client.AskTxtContext(ctx, command)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", command, err)
		}
		id, err := strconv.ParseInt(strings.TrimSpace(reply), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid object ID %q: %w", command, reply, err)
		}
		if id == 0 {
			return result, nil
		}
		if seen[id] || ancestors[id] || (!parent.root && id == parent.id) {
			return nil, fmt.Errorf("%s: repeated or cyclic object ID %d", command, id)
		}
		seen[id] = true
		name, err := client.AskTxtContext(ctx, fmt.Sprintf("#%d.name", id))
		if err != nil {
			return nil, fmt.Errorf("name of #%d: %w", id, err)
		}
		result = append(result, object{id: id, name: name, path: parent.path + "/" + name})
		previous = id
	}
}
