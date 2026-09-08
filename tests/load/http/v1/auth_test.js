// noinspection JSUnusedGlobalSymbols

import http from 'k6/http';
import { check, fail, group, sleep } from 'k6';
import { Counter, Rate } from 'k6/metrics';
import * as helpers from '../../helpers.js';

const successRate = new Rate('success_rate');
const connectionErrors = new Counter('connection_errors');
const operations = new Counter('operations');
const completedFlows = new Counter('completed_flows');

const ADDR = helpers.HTTPServerAddress();
const API_KEY = helpers.GRPCAPIKey();
const PASSWORD = 'LoadTest-Only!8qP3mZ7sK4vN2xR6';
const requiredOperations = [
    'signup', 'login', 'get_me', 'update_me',
    'invalidated_access', 'invalidated_refresh', 'login_updated', 'get_updated_me',
    'refresh', 'get_refreshed_me', 'logout', 'logged_out_access', 'logged_out_refresh', 'list_users',
];
const expectedResponses = {
    200: http.expectedStatuses(200),
    201: http.expectedStatuses(201),
    401: http.expectedStatuses(401),
};

export const options = {
    scenarios: {
        auth_flow: {
            executor: 'ramping-vus',
            exec: 'authFlow',
            startVUs: 1,
            stages: [
                { duration: '10s', target: 2 },
                { duration: '20s', target: 5 },
                { duration: '10s', target: 1 },
            ],
        },
        api_operations: {
            executor: 'constant-arrival-rate',
            exec: 'apiOperations',
            rate: 2,
            timeUnit: '1s',
            duration: '40s',
            preAllocatedVUs: 2,
            maxVUs: 5,
        },
    },
    thresholds: {
        http_req_duration: ['p(95)<500'],
        http_req_failed: ['rate==0'],
        checks: ['rate==1'],
        success_rate: ['rate==1'],
        connection_errors: ['count==0'],
        dropped_iterations: ['count==0'],
        'completed_flows{flow:auth}': ['count>0'],
        'completed_flows{flow:api}': ['count>0'],
    },
};

for (const operation of requiredOperations) {
    options.thresholds[`operations{operation:${operation}}`] = ['count>0'];
}

export function setup() {
    connectionErrors.add(0);
    completedFlows.add(0, { flow: 'auth' });
    completedFlows.add(0, { flow: 'api' });
    // Emit zero samples so skipped operations fail their coverage thresholds.
    for (const operation of requiredOperations) {
        operations.add(0, { operation });
    }
}

export function authFlow() {
    runFlow('auth', () => {
        const user = signup('auth');
        let tokens = login(user.email);
        getMe('get_me', tokens.access_token, user);

        const updatedEmail = `updated-${user.email}`;
        request('update_me', 'PUT', '/users/me', {
            accessToken: tokens.access_token,
            body: { email: updatedEmail, current_password: PASSWORD },
        });
        assertSessionRejected('invalidated', tokens);

        user.email = updatedEmail;
        tokens = login(user.email, 'login_updated');
        getMe('get_updated_me', tokens.access_token, user);

        const refreshed = request('refresh', 'POST', '/auth/refresh', {
            body: { token: tokens.refresh_token },
            validate: (body) => hasTokens(body) && body.refresh_token !== tokens.refresh_token,
        });
        getMe('get_refreshed_me', refreshed.access_token, user);
        request('logout', 'POST', '/auth/logout', {
            body: { refresh_token: refreshed.refresh_token },
        });
        assertSessionRejected('logged_out', refreshed);
    });
}

// Each read VU owns its session; credential updates happen in the auth scenario.
let reader;

export function apiOperations() {
    runFlow('api', () => {
        if (!reader) {
            const user = signup('api');
            reader = { user, tokens: login(user.email) };
        }
        getMe('get_me', reader.tokens.access_token, reader.user);
        request('list_users', 'GET', '/users?limit=10&offset=0', {
            headers: { 'X-API-Key': API_KEY },
            validate: (body) => Array.isArray(body) && body.length > 0 && body.length <= 10 &&
                body.every((user) => typeof user.uuid === 'string' && typeof user.email === 'string'),
        });
    });
}

function runFlow(name, run) {
    let completed = false;
    try {
        group(name, run);
        completed = true;
    } finally {
        completedFlows.add(Number(completed), { flow: name });
        check(completed, { [`${name} flow completed`]: (value) => value });
    }
    sleep(0.5);
}

function signup(prefix) {
    const email = `load-${prefix}-${__VU}-${__ITER}-${helpers.GenerateRandomString()}@example.com`.toLowerCase();
    return request('signup', 'POST', '/auth/signup', {
        body: { email, password: PASSWORD },
        status: 201,
        validate: (body) => body && typeof body.uuid === 'string' && body.uuid.length > 0 && body.email === email,
    });
}

function login(email, operation = 'login') {
    return request(operation, 'POST', '/auth/login', {
        body: { email, password: PASSWORD },
        validate: hasTokens,
    });
}

function hasTokens(body) {
    return body && typeof body.access_token === 'string' && body.access_token.length > 0 &&
        typeof body.refresh_token === 'string' && body.refresh_token.length > 0;
}

function getMe(operation, accessToken, user) {
    request(operation, 'GET', '/users/me', {
        accessToken,
        validate: (body) => body && body.uuid === user.uuid && body.email === user.email,
    });
}

function assertSessionRejected(prefix, tokens) {
    request(`${prefix}_access`, 'GET', '/users/me', { accessToken: tokens.access_token, status: 401 });
    request(`${prefix}_refresh`, 'POST', '/auth/refresh', {
        body: { token: tokens.refresh_token },
        status: 401,
    });
}

function request(operation, method, path, { body, accessToken, headers = {}, status = 200, validate } = {}) {
    const params = {
        headers: { 'Content-Type': 'application/json', ...headers },
        tags: { name: operation },
        timeout: '5s',
        redirects: 0,
        // Expected rejection checks must not inflate the HTTP failure rate.
        responseCallback: expectedResponses[status],
    };
    if (accessToken) {
        params.headers.Authorization = `Bearer ${accessToken}`;
    }

    const response = http.request(method, `${ADDR}/api/v1${path}`, body ? JSON.stringify(body) : null, params);
    let data = null;
    if (validate) {
        try {
            data = response.json();
        } catch (_) {
            // The response check below reports invalid JSON as a failure.
        }
    }
    const assertions = { [`${operation}: status ${status}`]: (res) => res.status === status };
    if (validate) {
        assertions[`${operation}: response matches`] = () => validate(data);
    }
    const success = check(response, assertions, { operation });
    operations.add(1, { operation });
    connectionErrors.add(Number(response.status === 0));
    successRate.add(success, { operation });
    if (!success) {
        fail(`${operation}: expected status ${status} and a valid response, received status ${response.status}`);
    }
    return data;
}
