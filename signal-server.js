const express = require('express');
const expressWs = require('express-ws');
const crypto = require('crypto');
const path = require('path');

const PORT = process.env.PORT || 8080;
const DASHBOARD_USER = process.env.DASHBOARD_USER || 'admin';
const DASHBOARD_PASS = process.env.DASHBOARD_PASS || crypto.randomBytes(8).toString('hex');

const app = express();
expressWs(app);

const agents = new Map();
const pending = new Map();

// Basic Auth middleware
function basicAuth(req, res, next) {
    const auth = req.headers.authorization;
    if (!auth) {
        res.setHeader('WWW-Authenticate', 'Basic realm="Agent Control"');
        return res.status(401).send('Authentication required');
    }
    const base64 = auth.split(' ')[1];
    const [user, pass] = Buffer.from(base64, 'base64').toString().split(':');
    if (user === DASHBOARD_USER && pass === DASHBOARD_PASS) return next();
    res.setHeader('WWW-Authenticate', 'Basic realm="Agent Control"');
    res.status(401).send('Invalid credentials');
}

// Serve the standalone HTML dashboard
app.get('/', basicAuth, (req, res) => {
    res.sendFile(path.join(__dirname, 'dashboard.html'));
});

// WebSocket endpoints (unchanged)
app.ws('/control', (ws, req) => {
    const interval = setInterval(() => {
        if (ws.readyState === ws.OPEN) {
            ws.send(JSON.stringify({ type: 'agents_update', agents: Array.from(agents.keys()) }));
        }
    }, 2000);
    ws.on('close', () => clearInterval(interval));
    ws.on('message', async (msg) => {
        try {
            const cmd = JSON.parse(msg);
            if (cmd === 'ping') return;
            const agent = agents.get(cmd.targetId);
            if (!agent) {
                ws.send(JSON.stringify({ error: 'Agent offline', requestId: cmd.requestId }));
                return;
            }
            pending.set(cmd.requestId, { controlWs: ws, agentId: cmd.targetId });
            agent.send(JSON.stringify(cmd));
            setTimeout(() => pending.delete(cmd.requestId), 30000);
        } catch (err) {}
    });
});

app.ws('/agent', (ws, req) => {
    let agentId = null;
    ws.on('message', (msg) => {
        try {
            const data = JSON.parse(msg);
            if (data.type === 'register') {
                agentId = data.clientId;
                agents.set(agentId, ws);
                console.log(`✅ Agent registered: ${agentId}`);
                return;
            }
            if (data.requestId && pending.has(data.requestId)) {
                const { controlWs } = pending.get(data.requestId);
                if (controlWs.readyState === controlWs.OPEN) {
                    controlWs.send(JSON.stringify(data));
                }
            }
        } catch (err) {}
    });
    ws.on('close', () => {
        if (agentId) agents.delete(agentId);
        console.log(`❌ Agent disconnected: ${agentId}`);
    });
});

app.listen(PORT, () => {
    console.log(`🚀 Signaling server running on port ${PORT}`);
    console.log(`🔐 Dashboard credentials: ${DASHBOARD_USER} / ${DASHBOARD_PASS}`);
    console.log(`📡 Agent endpoint: ws://localhost:${PORT}/agent`);
});