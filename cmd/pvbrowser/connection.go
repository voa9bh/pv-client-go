package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type ownedBrowserClient interface {
	browserClient
	Close() error
}

type clientFactory func(string) (ownedBrowserClient, error)

func (b *browser) closeClient(client ownedBrowserClient) {
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		if err := client.Close(); err != nil {
			b.closeMu.Lock()
			b.closeErr = errors.Join(b.closeErr, err)
			b.closeMu.Unlock()
			b.post(result{kind: clientCloseResult, err: err})
		}
	}()
}

func (b *browser) promptPort() {
	if b.newClient == nil {
		b.setStatus("Changing ports is unavailable for this client")
		return
	}
	_, port, err := net.SplitHostPort(b.address)
	if err != nil {
		b.setStatus("Invalid current endpoint: " + err.Error())
		return
	}
	field := tview.NewInputField().SetLabel("Port: ").SetText(port)
	message := tview.NewTextView().SetText("Keep current host; cancel pending network operations.")
	form := tview.NewForm().AddFormItem(field)
	form.SetBorder(true).SetTitle(" Change port ")
	cancel := func() { b.closeMacroDialog() }
	submit := func() {
		port, err := parsePort(field.GetText())
		if err != nil {
			message.SetText(err.Error())
			return
		}
		b.closeMacroDialog()
		b.changePort(port)
	}
	form.AddButton("Connect", submit).AddButton("Cancel", cancel).SetCancelFunc(cancel)
	field.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEnter:
			submit()
			return nil
		case tcell.KeyEscape:
			cancel()
			return nil
		}
		return event
	})
	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).AddItem(message, 2, 0, false)
	b.showMacroDialog(content)
}

func (b *browser) changePort(port string) {
	host, _, err := net.SplitHostPort(b.address)
	if err != nil {
		b.setStatus("Invalid current endpoint: " + err.Error())
		return
	}
	address := net.JoinHostPort(host, port)
	if address == b.address {
		b.startConnection(true)
		return
	}
	client, err := b.newClient(address)
	if err != nil {
		b.setStatus("Cannot create connection: " + err.Error())
		return
	}
	old := b.ownedClient
	b.stopMacro("Port change requested")
	b.client, b.ownedClient, b.address = client, client, address
	b.startConnection(false)
	if old != nil {
		b.closeClient(old)
	}
}

func (b *browser) startConnection(reconnect bool) {
	if b.connectCancel != nil {
		b.connectCancel()
	}
	b.stopMacro("Reconnect requested")
	b.ready, b.connecting = false, true
	b.connectionErr = nil
	b.reset()
	b.tree.SetTitle(" Objects | Connecting... ")
	b.setStatus("Connecting...")
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	b.connectCancel = cancel
	generation, client := b.generation, b.client
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		defer cancel()
		var err error
		if reconnect {
			err = client.Reconnect(ctx)
		} else {
			err = client.Connect(ctx)
		}
		b.post(result{generation: generation, err: err})
	}()
}

func (b *browser) connectionFailed(err error) {
	b.ready = false
	b.connectionErr = err
	b.root.state = failed
	b.root.node.ClearChildren()
	b.root.node.SetText("/pvServer [connection failed - R retries, P changes port]")
	b.tree.SetTitle(" Objects | CONNECTION FAILED ")
	b.updateDetails()
	b.setStatus(fmt.Sprintf("Connection failed: %v; R retries, P changes port", err))
}
