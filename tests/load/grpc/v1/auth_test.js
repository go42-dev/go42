// noinspection JSUnusedGlobalSymbols

import grpc from 'k6/net/grpc';
import { check, fail, group, sleep } from 'k6';
import { Counter, Rate } from 'k6/metrics';
import * as helpers from '../../helpers.js';

const successRate = new Rate('success_rate');
const connectionErrors = new Counter('connection_errors');
const authErrors = new Counter('auth_errors');
const operations = new Counter('operations');
const completedFlows = new Counter('completed_flows');

const ADDR = helpers.GRPCServerAddress();
const API_KEY = helpers.GRPCAPIKey();
const PASSWORD = 'LoadTest-Only!8qP3mZ7sK4vN2xR6';
const requiredOperations = [
    'create_user', 'get_user', 'update_user', 'get_updated_user', 'list_users', 'delete_user', 'get_deleted_user',
];

const client = new grpc.Client();
client.load(['../../../../api/proto'], 'auth/v1/auth.proto');

export const options = {
    scenarios: {
        user_management: {
            executor: 'ramping-vus',
            startVUs: 1,
            stages: [
                { duration: '10s', target: 2 },
                { duration: '20s', target: 3 },
                { duration: '10s', target: 1 },
            ],
        },
    },
    thresholds: {
        grpc_req_duration: ['p(95)<300'],
        checks: ['rate==1'],
        success_rate: ['rate==1'],
        connection_errors: ['count==0'],
        auth_errors: ['count==0'],
        completed_flows: ['count>0'],
    },
};

for (const operation of requiredOperations) {
    options.thresholds[`operations{operation:${operation}}`] = ['count>0'];
}

export function setup() {
    connectionErrors.add(0);
    authErrors.add(0);
    completedFlows.add(0);
    // Emit zero samples so skipped operations fail their coverage thresholds.
    for (const operation of requiredOperations) {
        operations.add(0, { operation });
    }
}

export default function () {
    let connected = false;
    let completed = false;
    try {
        try {
            client.connect(ADDR, { plaintext: true, timeout: '5s' });
            connected = true;
        } catch (error) {
            connectionErrors.add(1);
            fail(`gRPC connection failed: ${error.message}`);
        }
        group('User management', () => {
            const email = `load-grpc-${__VU}-${__ITER}-${helpers.GenerateRandomString()}@example.com`.toLowerCase();
            const created = invoke('create_user', 'CreateUser', { email, password: PASSWORD }, {
                validate: (body) => body && body.user && typeof body.user.uuid === 'string' &&
                    body.user.uuid.length > 0 && body.user.email === email,
            });
            const uuid = created.user.uuid;
            invoke('get_user', 'GetUserByUUID', { uuid }, {
                validate: (body) => matchesUser(body, uuid, email),
            });

            const updatedEmail = `updated-${email}`;
            invoke('update_user', 'UpdateUser', { uuid, email: updatedEmail });
            invoke('get_updated_user', 'GetUserByUUID', { uuid }, {
                validate: (body) => matchesUser(body, uuid, updatedEmail),
            });
            invoke('list_users', 'ListUsers', { limit: 10, offset: 0 }, {
                validate: (body) => body && Array.isArray(body.users) && body.users.length > 0 &&
                    body.users.length <= 10 && body.users.every((user) =>
                        typeof user.uuid === 'string' && typeof user.email === 'string'),
            });
            invoke('delete_user', 'DeleteUser', { uuid });
            invoke('get_deleted_user', 'GetUserByUUID', { uuid }, { status: grpc.StatusNotFound });
        });
        completed = true;
    } finally {
        completedFlows.add(Number(completed));
        check(completed, { 'user management flow completed': (value) => value });
        if (connected) {
            client.close();
        }
    }
    sleep(0.5);
}

function matchesUser(body, uuid, email) {
    return body && body.user && body.user.uuid === uuid && body.user.email === email;
}

function invoke(operation, method, data, { status = grpc.StatusOK, validate } = {}) {
    let response;
    try {
        response = client.invoke(`auth.v1.AuthService/${method}`, data, {
            metadata: { 'x-api-key': API_KEY },
            tags: { rpc: method, name: operation },
            timeout: '5s',
        });
    } catch (error) {
        operations.add(1, { operation });
        connectionErrors.add(1);
        successRate.add(false, { operation });
        fail(`${operation} failed: ${error.message}`);
    }
    const assertions = { [`${operation}: status ${status}`]: (res) => res.status === status };
    if (validate) {
        assertions[`${operation}: response matches`] = (res) => validate(res.message);
    }
    const success = check(response, assertions, { operation });
    operations.add(1, { operation });
    connectionErrors.add(Number(response.status === grpc.StatusUnavailable || response.status === grpc.StatusDeadlineExceeded));
    authErrors.add(Number(response.status === grpc.StatusUnauthenticated || response.status === grpc.StatusPermissionDenied));
    successRate.add(success, { operation });
    if (!success) {
        fail(`${operation}: expected status ${status} and a valid response, received status ${response.status}`);
    }
    return response.message;
}
