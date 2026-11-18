package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type member struct {
	handle                      int64
	name, description           string
	security                    int64
	dimension                   int64
	datatype, request, response string
}

type metadata struct {
	class                  int64
	properties, methods    []member
	propertyErr, methodErr error
}

func parseMembers(text string, methods bool) ([]member, error) {
	tokens := strings.Split(text, ";")
	var columns [6][]string
	pos, rows := 0, -1
	for column := range columns {
		if pos >= len(tokens) {
			return nil, fmt.Errorf("missing count for column %d", column+1)
		}
		count, err := strconv.Atoi(tokens[pos])
		pos++
		if err != nil || count < 0 || count > len(tokens)-pos {
			return nil, fmt.Errorf("invalid count in column %d", column+1)
		}
		if rows >= 0 && rows != count {
			return nil, fmt.Errorf("column %d count differs from handle count", column+1)
		}
		rows = count
		columns[column] = tokens[pos : pos+count]
		pos += count
	}
	if pos != len(tokens) {
		return nil, fmt.Errorf("unexpected metadata tokens (unescaped semicolon?)")
	}
	list := make([]member, rows)
	for i := range list {
		m := &list[i]
		var err error
		m.handle, err = strconv.ParseInt(columns[0][i], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("row %d: invalid member handle: %w", i+1, err)
		}
		m.name, m.description = columns[1][i], columns[2][i]
		m.security, err = strconv.ParseInt(columns[3][i], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("row %d: invalid security: %w", i+1, err)
		}
		if methods {
			m.request, m.response = columns[4][i], columns[5][i]
		} else {
			m.dimension, err = strconv.ParseInt(columns[4][i], 10, 64)
			if err != nil || m.dimension < 0 {
				return nil, fmt.Errorf("row %d: invalid dimension", i+1)
			}
			m.datatype = columns[5][i]
		}
	}
	return list, nil
}

func fetchMetadata(ctx context.Context, client textClient, o object) (metadata, error) {
	if err := ctx.Err(); err != nil {
		return metadata{}, err
	}
	command := o.address() + ".classHandle"
	reply, err := client.AskTxtContext(ctx, command)
	if err != nil {
		return metadata{}, fmt.Errorf("%s: %w", command, err)
	}
	class, err := strconv.ParseInt(strings.TrimSpace(reply), 10, 64)
	if err != nil {
		return metadata{}, fmt.Errorf("invalid classHandle %q: %w", reply, err)
	}
	info := metadata{class: class}
	for _, methods := range []bool{false, true} {
		method := "getPropertyInfos"
		if methods {
			method = "getMethodInfos"
		}
		command = fmt.Sprintf("%s.%s(%d)", o.address(), method, class)
		var members []member
		err = ctx.Err()
		if err == nil {
			reply, err = client.AskTxtContext(ctx, command)
		}
		if err == nil {
			members, err = parseMembers(reply, methods)
		}
		if err != nil {
			err = fmt.Errorf("%s: %w", command, err)
		}
		if methods {
			info.methods, info.methodErr = members, err
		} else {
			info.properties, info.propertyErr = members, err
		}
	}
	return info, nil
}
