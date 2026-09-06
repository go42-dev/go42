-- +goose Up

create table if not exists auth_users (
    id integer primary key autoincrement,
    uuid text not null unique,
    password text,
    credential_version integer not null default 1,
    email text not null unique,
    status text not null default 'active',
    is_system integer not null default 0,
    metadata text,
    created_at datetime not null default current_timestamp,
    updated_at datetime not null default current_timestamp,
    deleted_at datetime
);

create index if not exists idx_auth_users_uuid on auth_users (uuid);
create index if not exists idx_auth_users_email on auth_users (email);
create index if not exists idx_auth_users_deleted_at on auth_users (deleted_at);

create table if not exists auth_sessions (
    id text primary key,
    user_id integer not null,
    credential_version integer not null,
    refresh_token_id text not null,
    expires_at datetime not null,
    revoked_at datetime null,
    created_at datetime not null default current_timestamp,
    updated_at datetime not null default current_timestamp,
    constraint fk_auth_sessions_user foreign key (user_id) references auth_users (
        id
    ) on delete cascade
);

create index idx_auth_sessions_user_id on auth_sessions (user_id);
create index idx_auth_sessions_expires_at on auth_sessions (expires_at);

create table if not exists auth_users_history
(
    id text primary key,
    occurred_at datetime not null,
    created_at datetime default (datetime('now')) not null,
    user_id integer not null,
    event_type varchar(255) not null,
    data varchar(255),
    metadata varchar(1000),
    foreign key (user_id) references auth_users (id) on delete cascade
);

create index if not exists idx_auth_users_history_occurred_at on auth_users_history (
    occurred_at
);

create table if not exists auth_roles (
    id integer primary key autoincrement,
    name text not null unique,
    description text not null default '',
    is_system integer not null default 0,
    created_at datetime not null default current_timestamp,
    updated_at datetime not null default current_timestamp,
    deleted_at datetime
);

create index if not exists idx_auth_roles_deleted_at on auth_roles (deleted_at);

create table if not exists auth_permissions (
    id integer primary key autoincrement,
    resource text not null,
    action text not null,
    created_at datetime not null default current_timestamp,
    unique (resource, action)
);

create table if not exists auth_role_permissions (
    role_id integer not null,
    permission_id integer not null,
    primary key (role_id, permission_id),
    foreign key (role_id) references auth_roles (id) on delete cascade,
    foreign key (permission_id) references auth_permissions (
        id
    ) on delete cascade
);

create index if not exists idx_auth_role_permissions_permission_id on auth_role_permissions (
    permission_id
);

create table if not exists auth_user_roles (
    user_id integer not null,
    role_id integer not null,
    created_at datetime not null default current_timestamp,
    expires_at datetime,
    primary key (user_id, role_id),
    foreign key (user_id) references auth_users (id) on delete cascade,
    foreign key (role_id) references auth_roles (id) on delete cascade
);

create index if not exists idx_auth_user_roles_role_id on auth_user_roles (
    role_id
);
create index if not exists idx_auth_user_roles_expires_at on auth_user_roles (
    expires_at
);

create table if not exists auth_api_tokens (
    id integer primary key autoincrement,
    uuid text not null unique,
    user_id integer not null,
    token text not null unique,
    name text not null,
    last_used_at datetime default null,
    expires_at datetime default null,
    created_at datetime default current_timestamp,
    updated_at datetime default current_timestamp,
    deleted_at datetime default null,
    constraint fk_api_tokens_user foreign key (user_id) references auth_users (
        id
    ) on delete cascade
);

create index idx_token_lookup on auth_api_tokens (
    token, deleted_at, expires_at
);

create table if not exists auth_api_tokens_permissions (
    token_id integer not null,
    permission_id integer not null,
    primary key (token_id, permission_id),
    foreign key (token_id) references auth_api_tokens (id) on delete cascade,
    foreign key (permission_id) references auth_permissions (
        id
    ) on delete cascade
);

create index if not exists idx_token_permissions_token_id on auth_api_tokens_permissions (
    token_id
);
create index if not exists idx_token_permissions_permission_id on auth_api_tokens_permissions (
    permission_id
);

-- +goose Down

drop table if exists auth_api_tokens_permissions;
drop table if exists auth_api_tokens;
drop table if exists auth_user_roles;
drop table if exists auth_role_permissions;
drop table if exists auth_permissions;
drop table if exists auth_roles;
drop table if exists auth_users_history;
drop table if exists auth_sessions;
drop table if exists auth_users;
