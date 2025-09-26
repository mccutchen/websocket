import exec from "k6/execution";
import { check, fail } from "k6";
import { WebSocket } from "k6/experimental/websockets";
import { Trend } from "k6/metrics";
import { randomString } from "https://jslib.k6.io/k6-utils/1.1.0/index.js";

const cfg = {
    messageSize: parseInt(__ENV.MESSAGE_SIZE), // Size of each message in bytes
    targetURL: __ENV.TARGET_URL,
};

// custom metric for tracking RTT for a single message to be echoed by the
// server
const wsMessageRTT = new Trend("ws_msg_rtt", true);

export default function () {
    runEchoLoadTest(cfg.targetURL, cfg.messageSize);
}

// runEchoLoadTest applies load to the target URL by having each VU generate a
// random message of the given size and then send it to the server in a loop,
// verifying that it receives the same message in reply before sending it
// again.
function runEchoLoadTest(targetURL, messageSize) {
    // each VU will repeatedly send the same random message
    const msg = randomString(messageSize);

    // track last sent timestamp to record custom RTT metric
    let sentAt;

    const ws = new WebSocket(targetURL);

    ws.addEventListener("open", () => {
        // we're connected, send first message to kick off load test
        sentAt = Date.now();
        ws.send(msg);
    });

    ws.addEventListener("message", (event) => {
        wsMessageRTT.add(Date.now() - sentAt);

        // verify expected response
        check(event.data, {
            "received expected response": (data) => data === msg,
        });

        // if the test run is finished (i.e. progress == 100%), close the
        // connection to let the VU exit gracefully
        if (exec.scenario.progress >= 1) {
            ws.close();
            return;
        }

        // otherwise, immediately send another message
        sentAt = Date.now();
        ws.send(msg);
    });

    ws.addEventListener("error", (event) => {
        fail(event.error);
    });
}
