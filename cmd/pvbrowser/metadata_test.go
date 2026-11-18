package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const propertySample = "11;1;-1;-2;-3;-4;-5;-6;-7;-8;-9;-10;11;value;classHandle;parentHandle;handle;forwardHandle;gatewayHandle;locked;simulation;security;dimension;name;11;integer value;Property classHandle;Property parentHandle;Property handle;Property forwardHandle;Property gatewayHandle;Property locked;Property simulation;Property security;Property dimension;Property name;11;0;0;0;0;0;0;0;0;0;0;0;11;0;0;0;0;0;0;0;0;0;0;0;11;l;l;l;l;l;l;l;l;l;l;t"
const oneProperty = "1;-1;1;value;1;A property;1;0;1;0;1;t"
const oneMethod = "1;-2;1;set;1;Sets value;1;0;1;t;1;"

func TestParseMetadata(t *testing.T) {
	properties, err := parseMembers(propertySample, false)
	if err != nil || len(properties) != 11 {
		t.Fatalf("supplied properties: %+v, %v", properties, err)
	}
	if properties[0].name != "value" || properties[10].handle != -10 || properties[10].datatype != "t" {
		t.Fatalf("misaligned properties: %+v", properties)
	}
	methods, err := parseMembers("2;1;-34;2;init;getMethods;2;initialize;returns methods;2;0;2;2;;C;2;;nlnt", true)
	if err != nil || len(methods) != 2 || methods[0].request != "" || methods[0].response != "" ||
		methods[1].request != "C" || methods[1].response != "nlnt" || methods[1].security != 2 {
		t.Fatalf("methods: %+v, %v", methods, err)
	}
	methods, err = parseMembers(oneMethod, true)
	if err != nil || methods[0].response != "" {
		t.Fatalf("trailing empty format lost: %+v %v", methods, err)
	}
	for _, methods := range []bool{false, true} {
		list, err := parseMembers("0;0;0;0;0;0", methods)
		if err != nil || len(list) != 0 {
			t.Fatalf("empty metadata: %+v, %v", list, err)
		}
	}
}

func TestMalformedMetadata(t *testing.T) {
	for _, sample := range []string{
		"", "-1", "9999999999999999999999", "2;-1", "1;invalid;1;n;1;d;1;0;1;0;1;t",
		"1;9223372036854775808;1;n;1;d;1;0;1;0;1;t",
		"1;-1;1;n;1;d;1;bad;1;0;1;t", "1;-1;1;n;1;d;1;0;1;-1;1;t",
		"1;-1;0;1;d;1;0;1;0;1;t", "1;-1;1;n;1;d;1;0;1;0;1",
		oneProperty + ";", strings.Replace(oneProperty, "A property", "embedded;semicolon", 1),
	} {
		if _, err := parseMembers(sample, false); err == nil {
			t.Fatalf("accepted malformed %q", sample)
		}
	}
}

func TestFetchMetadata(t *testing.T) {
	var calls []string
	client := &fakeClient{metadata: func(ctx context.Context, command string) (string, error) {
		calls = append(calls, command)
		switch {
		case strings.HasSuffix(command, ".classHandle"):
			return " 181\n", nil
		case strings.Contains(command, ".getPropertyInfos("):
			return oneProperty, nil
		default:
			return oneMethod, nil
		}

	}}
	info, err := fetchMetadata(context.Background(), client, object{id: -1})
	want := []string{"#-1.classHandle", "#-1.getPropertyInfos(181)", "#-1.getMethodInfos(181)"}
	if err != nil || info.class != 181 || info.propertyErr != nil || info.methodErr != nil ||
		len(info.properties) != 1 || len(info.methods) != 1 || !reflect.DeepEqual(calls, want) {
		t.Fatalf("%+v %v %v", info, err, calls)
	}
	remote := errors.New("remote failure")
	client.metadata = func(ctx context.Context, command string) (string, error) {
		if strings.HasSuffix(command, ".classHandle") {
			return "181", nil
		}
		if strings.Contains(command, ".getPropertyInfos(") {
			return "", remote
		}
		return oneMethod, nil
	}
	info, err = fetchMetadata(context.Background(), client, rootObject())
	if err != nil || !errors.Is(info.propertyErr, remote) || len(info.methods) != 1 {
		t.Fatalf("partial results: %+v %v", info, err)
	}
	client.metadata = func(context.Context, string) (string, error) { return "", remote }
	if _, err := fetchMetadata(context.Background(), client, rootObject()); !errors.Is(err, remote) {
		t.Fatalf("class query: %v", err)
	}
	client.metadata = func(context.Context, string) (string, error) { return "not a class", nil }
	if _, err := fetchMetadata(context.Background(), client, rootObject()); err == nil {
		t.Fatal("accepted invalid class")
	}
}

func TestMetadataCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeClient{metadata: func(context.Context, string) (string, error) {
		t.Fatal("metadata queried after cancellation")
		return "", nil
	}}
	if _, err := fetchMetadata(ctx, client, rootObject()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
