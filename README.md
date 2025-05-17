# Caddy Notifier

Design under construction...

## Design

The caddy-notifier works as WebSocket consolidator, that consolidate multiple WebSocket connections into logical channels.

1. To reduce the number reverse connection from caddy to backend
2. Simplifies message distribution workflows

```mermaid
flowchart TD
    C[subscriber] <--websocket/subscribe--> A
    A[caddy-notifier] <--authenticate/subscribe/publish--> B[backend endpoint]
    subgraph backend
    B
    end
    subgraph caddy-server
    A 
    end
```

Subscribers are connected via WebSocket interface and subscriber is able to subscribe to specific channel via subscribe event. When subscriber subscribes a channel successfully, the subscriber will receive events from subscribed channel.

Backend endpoint are connected via WebSocket and responsible to authenticate subscribers and publish event to channels.

### Considerations

1. When subscriber disconnected from the notifier, it needs to re-subscribe all channels needed, to eliminate requirement of session storage (eliminate stateful session)
2. For subscriber (? maybe certain optional parameter `after` can be added to receive buffered message. How it defines? If timestamped, we need to ensure time sync, If message id, we need to ensure it incremental correctly. Or we just send all buffered message and let subscriber deduplicate)
3. Metrics (prefix `caddy_websocket_notifier_*`)
   1. Number of message sent (`message_sent_total`)
   2. number of subscribe event (`subscribe_requested_total`)
   3. current connection count (`active_connection`)
   4. current channel count (`channel_count`)
   5. upstream status (`upstream_healthy`)
   6. inbound message size (`websocket_inbound_bytes_total`)
   7. outbound message size (`websocket_outbound_bytes_total`)
   8. outbound compressed message size (`websocket_outbound_compressed_bytes_total`)

### Safety Considerations

1. Although `de-authorize` provided method to ensure no left-over subscriptions, token that expires by time may need extra watch dog (? or provide `valid_until` field in authenticator response)
2. Maybe need to limit the number of channel single connection can connect to
3. Do we allow multiple connection to use the same credential? (maybe from different page, or consolidate via web worker)
4. Do we want to limit the rate of sending out events? Due to event fan-out nature, there must be write-amplification effect. In this case, do we consider certain event have higher priority or certain event could be dropped when rate limited.
5. Do we want to limit the subscribe rate for single connection?

### Packages

- [gorilla/websocket](https://github.com/gorilla/websocket) will be the key package to upgrade incoming and outbound connections, for its ease of use and reliability. Although [nbio](https://github.com/lesismal/nbio) claimed to be able to handle 1 M connections with non-blocking strategies. It needs special listener, which is essentially incompatible with the Caddy setup.

### Caddyfile

```caddyfile
{
    metrics
}

:6080 {
    websocket_notifier /ws "ws://localhost:6081/ws" {
        write_wait 10s
        pong_wait 60s
        ping_interval 50s
        max_message_size 256k # max incoming message size from subscriber
        chan_size 16
        recovery_wait 5s

        header_up +TEST_HEADER "value"
        header_down -TEST_HEADER "value"

        compression shorty
        shorty_reset_count 1000
    }
}
```

## TODO

- [x] De-authorize
- [x] Metrics
- [ ] Rate limit (?)
- [ ] Message Buffer

### Protocol

The client sent of `ping` message will be treated as `ping`, and the client sent of `shorty` message will be treated as `shorty` compression enable operation.

#### subscriber to caddy-notifier

##### subscribe

```json
{
    "operation": "subscribe",
    "credential": "a string token for authentication",
    "channels": ["a list of string value for channel name"]
}
```

The caddy-notifier will request backend with credential to authenticate, whether subscribe request accepted or rejected. Channel specified channel to be subscribed. (? or reject directly if reach certain subscription limit).

##### unsubscribe

```json
{
    "operation": "unsubscribe",
    "channels": ["a list of string values for channel name"]
}
```

The caddy-notifier will remove the subscriber from the channel subscription. If certain channel is not subscribed, it will be no-op. After unsubscribe, the subscriber will no longer receive message from that channel.

#### caddy-notifier to subscriber

##### subscribe results

```json
{
    "operation": "subscribed",
    "channels": ["a list of channel_name"],
}
```

The caddy-notifier notifies the subscriber on the decision from the authenticator.

```json
{
    "operation": "unsubscribed",
    "channels": ["a list of channel_name"],
}
```

Rejected or de-authorized sent out `unsubscribed` events.

##### Events

```json
{
    "operation": "event",
    "channels": ["a list of channel_name"],
    "payload": { event content }
}
```

When a subscriber subscribes to multiple channel in the list, the event will be sent out once.

#### caddy-notifier to backend

##### subscribe

```json
{
    "connection_id": "identifier to distinct different connections",
    "operation": "subscribe",
    "channels": ["a list of channel_name"],
    "credential": "xxx"
}
```

? Do we want to notify backend about unsubscribes?

#### backend to caddy-notifier

##### subscribe result

```json
{
    "connection_id": "identifier to distinct different connections",
    "operation": "verify",
    "credential": "credential for the accepted channel, must present if deauthorize requested", 
    "accept": ["a list of channel_name of accepted channels"],
    "reject": ["a list of channel_name of rejected channels"],
}
```

##### event

```json
{
    "operation": "event",
    "channels": ["a list of channel_name"],
    "payload": { "event contents" }
}
```

caddy-notifier will notifier all subscriber in the specific channel (? or list of channels)

##### de-authorize

```json
{
    "operation": "deauthorize",
    "credential": "xxx"
}
```

De-authorize certain credential = unsubscribe from all channels for that credential. Ensure no subscription retained when credential expires / invalidated.
