package nodeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	openVPNSessionSeenInitialDelay = 7 * time.Second
	openVPNSessionSeenInterval     = 30 * time.Second
	openVPNManagementTimeout       = 3 * time.Second
	openVPNSessionCallbackTimeout  = 6 * time.Second
	openVPNSessionSeenConcurrency  = 8
)

func (s *Server) runOpenVPNSessionSeenLoop(
	tag,
	root string,
	done <-chan struct{},
) {
	configPath := filepath.Join(
		root,
		"session-helper.json",
	)

	raw, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			s.appendLog(
				"openvpn session helper read failed for " +
					tag + ": " + err.Error(),
			)
		}
		return
	}

	var cfg nativeSessionHelperConfig

	if err := json.Unmarshal(raw, &cfg); err != nil {
		s.appendLog(
			"openvpn session helper parse failed for " +
				tag + ": " + err.Error(),
		)
		return
	}

	if strings.TrimSpace(cfg.Callback.URL) == "" {
		return
	}

	timer := time.NewTimer(
		openVPNSessionSeenInitialDelay,
	)
	defer timer.Stop()

	for {
		select {
		case <-done:
			return

		case <-timer.C:
			if err := s.refreshOpenVPNSessions(
				tag,
				root,
				cfg,
			); err != nil {
				s.appendLog(
					"openvpn session refresh failed for " +
						tag + ": " + err.Error(),
				)
			}

			timer.Reset(
				openVPNSessionSeenInterval,
			)
		}
	}
}

func (s *Server) refreshOpenVPNSessions(
	tag,
	root string,
	cfg nativeSessionHelperConfig,
) error {
	statusPath := filepath.Join(
		root,
		"status.tsv",
	)

	raw, err := os.ReadFile(statusPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf(
			"read status: %w",
			err,
		)
	}

	clients, err := parseOpenVPNStatusV3(
		string(raw),
	)
	if err != nil {
		return err
	}

	inboundTag := strings.TrimSpace(
		cfg.InboundTag,
	)
	if inboundTag == "" {
		inboundTag = strings.TrimSpace(tag)
	}

	stateDir := strings.TrimSpace(
		cfg.StateDir,
	)
	if stateDir == "" {
		stateDir = filepath.Join(
			root,
			"sessions",
		)
	}

	sem := make(
		chan struct{},
		openVPNSessionSeenConcurrency,
	)

	var wg sync.WaitGroup

	for _, current := range clients {
		client := current

		username := strings.TrimSpace(
			client.Username,
		)

		userID := cfg.Users[username]

		if userID <= 0 {
			userID = cfg.Users[strings.TrimSpace(
				client.CommonName,
			)]
		}

		if userID <= 0 {
			continue
		}

		if s.enforceOpenVPNClientPolicy(
			tag,
			cfg,
			username,
			userID,
			client,
		) {
			continue
		}

		clientIP, trustedPort :=
			openVPNStatusRealAddressParts(
				client.RealAddress,
			)

		if trustedPort == "" {
			continue
		}

		stateKey := nativeSessionStateKey(
			inboundTag,
			userID,
			strings.TrimSpace(
				client.VirtualAddress,
			),
			clientIP,
			trustedPort,
		)

		statePath := filepath.Join(
			stateDir,
			stateKey+".session",
		)

		rawSessionID, err := os.ReadFile(
			statePath,
		)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			s.appendLog(
				"openvpn session state read failed for " +
					tag + ": " + err.Error(),
			)
			continue
		}

		sessionID := strings.TrimSpace(
			string(rawSessionID),
		)
		if sessionID == "" {
			continue
		}

		wg.Add(1)

		go func(
			client openVPNStatusClient,
			userID int64,
			sessionID,
			clientIP,
			trustedPort string,
		) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() {
				<-sem
			}()

			ctx, cancel := context.WithTimeout(
				context.Background(),
				openVPNSessionCallbackTimeout,
			)
			defer cancel()

			err := s.sendNativeSessionEvent(
				ctx,
				cfg.Callback,
				nativeSessionEvent{
					NodeID:     cfg.Callback.NodeID,
					UserID:     userID,
					Protocol:   "ov",
					InboundTag: inboundTag,
					SessionID:  sessionID,
					AssignedIP: strings.TrimSpace(
						client.VirtualAddress,
					),
					ClientIP: clientIP,
					Event:    "seen",
				},
			)

			if err == nil {
				return
			}

			if !errors.Is(
				err,
				errNativeSessionDeviceLimit,
			) {
				s.appendLog(
					"openvpn seen callback failed for " +
						tag + " user " +
						strconv.FormatInt(
							userID,
							10,
						) +
						": " + err.Error(),
				)
				return
			}

			clientID := strings.TrimSpace(
				client.ClientID,
			)
			if clientID == "" {
				s.appendLog(
					"openvpn device limit reached but client ID missing for " +
						tag,
				)
				return
			}

			if err := openVPNManagementClientKill(
				cfg.ManagementNetwork,
				cfg.ManagementAddress,
				clientID,
			); err != nil {
				s.appendLog(
					"openvpn client kill failed for " +
						tag + " cid " +
						clientID + ": " +
						err.Error(),
				)
				return
			}

			s.appendLog(
				"openvpn client disconnected by device limit: " +
					tag + " cid " +
					clientID,
			)
		}(
			client,
			userID,
			sessionID,
			clientIP,
			trustedPort,
		)
	}

	wg.Wait()

	return nil
}

func openVPNStatusRealAddressParts(
	raw string,
) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}

	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return raw, ""
	}

	return strings.TrimSpace(host),
		strings.TrimSpace(port)
}

func openVPNManagementClientKill(
	network,
	address,
	clientID string,
) error {
	network = strings.ToLower(
		strings.TrimSpace(network),
	)
	address = strings.TrimSpace(address)
	clientID = strings.TrimSpace(clientID)

	if network == "" {
		network = "unix"
	}

	switch network {
	case "unix", "tcp":
	default:
		return fmt.Errorf(
			"unsupported management network %q",
			network,
		)
	}

	if address == "" {
		return fmt.Errorf(
			"management address is empty",
		)
	}

	cid, err := strconv.ParseUint(
		clientID,
		10,
		64,
	)
	if err != nil {
		return fmt.Errorf(
			"invalid OpenVPN client ID %q",
			clientID,
		)
	}

	conn, err := net.DialTimeout(
		network,
		address,
		openVPNManagementTimeout,
	)
	if err != nil {
		return fmt.Errorf(
			"connect management interface: %w",
			err,
		)
	}
	defer conn.Close()

	if err := conn.SetDeadline(
		time.Now().Add(
			openVPNManagementTimeout,
		),
	); err != nil {
		return err
	}

	command := fmt.Sprintf(
		"client-kill %d\nquit\n",
		cid,
	)

	if _, err := io.WriteString(
		conn,
		command,
	); err != nil {
		return fmt.Errorf(
			"write management command: %w",
			err,
		)
	}

	response, readErr := io.ReadAll(
		io.LimitReader(
			conn,
			32*1024,
		),
	)

	text := string(response)

	if strings.Contains(
		text,
		"SUCCESS:",
	) && strings.Contains(
		strings.ToLower(text),
		"client-kill",
	) {
		return nil
	}

	if readErr != nil {
		return fmt.Errorf(
			"read management response: %w",
			readErr,
		)
	}

	scanner := bufio.NewScanner(
		strings.NewReader(text),
	)

	for scanner.Scan() {
		line := strings.TrimSpace(
			scanner.Text(),
		)

		if strings.HasPrefix(
			line,
			"ERROR:",
		) {
			return fmt.Errorf(
				"management interface: %s",
				line,
			)
		}
	}

	return fmt.Errorf(
		"management interface did not confirm client-kill",
	)
}
