native_speed_policy_stage() {
  local test_binary="$1" interface_name="$2" address="$3"
  [ -n "$test_binary" ] || return 0
  env ANTIMAGE_NATIVE_SPEED_INTERFACE="$interface_name" \
    ANTIMAGE_NATIVE_SPEED_ADDRESS="$address" \
    "$test_binary" -test.run='^TestNativeSpeedLimitNativeStage$' -test.v
}

native_speed_policy_cleanup_stage() {
  local test_binary="$1" interface_name="$2"
  [ -n "$test_binary" ] || return 0
  env ANTIMAGE_NATIVE_SPEED_INTERFACE="$interface_name" \
    "$test_binary" -test.run='^TestNativeSpeedLimitNativeCleanupStage$' -test.v
}

measure_native_tunnel_speed() {
  local label="$1" server_ip="$2" client_ip="$3" test_binary="$4" interface_name="$5"
  local port_up=19093 port_down=19094
  python3 "$SCRIPT_DIR/native-speed-transfer.py" receive "$server_ip" "$port_up" \
    "$ROOT/${label}-speed-upload.json" upload >"$ROOT/${label}-speed-upload.log" 2>&1 &
  local upload_receiver=$!
  PIDS+=("$upload_receiver")
  wait_for "$label upload speed receiver" sh -c "ss -lnt '( sport = :$port_up )' | grep -q $port_up"
  ip netns exec "$NS" python3 "$SCRIPT_DIR/native-speed-transfer.py" send "$server_ip" "$port_up"
  wait "$upload_receiver"
  cat "$ROOT/${label}-speed-upload.log"

  ip netns exec "$NS" python3 "$SCRIPT_DIR/native-speed-transfer.py" receive "$client_ip" "$port_down" \
    "$ROOT/${label}-speed-download.json" download >"$ROOT/${label}-speed-download.log" 2>&1 &
  local download_receiver=$!
  PIDS+=("$download_receiver")
  wait_for "$label download speed receiver" ip netns exec "$NS" sh -c "ss -lnt '( sport = :$port_down )' | grep -q $port_down"
  python3 "$SCRIPT_DIR/native-speed-transfer.py" send "$client_ip" "$port_down"
  wait "$download_receiver"
  cat "$ROOT/${label}-speed-download.log"
  native_speed_policy_cleanup_stage "$test_binary" "$interface_name"
}
