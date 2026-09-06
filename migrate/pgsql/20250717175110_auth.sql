-- +goose Up

create table if not exists auth_users (
    id bigserial primary key,
    uuid uuid not null,
    password varchar(255) null,
    credential_version bigint not null default 1,
    email varchar(255) not null,
    status varchar(255) not null default 'active',
    is_system boolean not null default false,
    metadata jsonb null,
    created_at timestamp not null default current_timestamp,
    updated_at timestamp not null default current_timestamp,
    deleted_at timestamp null
);

create unique index if not exists idx_auth_users_uuid on auth_users (uuid);
create unique index if not exists idx_auth_users_email on auth_users (email);
create index if not exists idx_auth_users_deleted_at on auth_users (deleted_at);

create table if not exists auth_sessions (
    id uuid primary key,
    user_id bigint not null,
    credential_version bigint not null,
    refresh_token_id uuid not null,
    expires_at timestamp not null,
    revoked_at timestamp null,
    created_at timestamp not null default current_timestamp,
    updated_at timestamp not null default current_timestamp,
    constraint fk_auth_sessions_user foreign key (user_id) references auth_users (
        id
    ) on delete cascade
);

create index idx_auth_sessions_user_id on auth_sessions (user_id);
create index idx_auth_sessions_expires_at on auth_sessions (expires_at);

create table if not exists auth_users_history
(
    id uuid primary key,
    occurred_at timestamp not null,
    created_at timestamp default now() not null,
    user_id integer not null,
    event_type varchar(255) not null,
    data varchar(255) null,
    metadata varchar(1000) null,
    constraint fk_auth_users_history_user_id foreign key (
        user_id
    ) references auth_users (id) on delete cascade
);

create index if not exists idx_auth_users_history_occurred_at on auth_users_history (
    occurred_at
);

create table if not exists auth_roles (
    id bigserial primary key,
    name varchar(50) not null,
    description varchar(255) not null default '',
    is_system boolean not null default false,
    created_at timestamp not null default current_timestamp,
    updated_at timestamp not null default current_timestamp,
    deleted_at timestamp null
);

create unique index if not exists idx_auth_roles_name on auth_roles (name);
create index if not exists idx_auth_roles_deleted_at on auth_roles (deleted_at);

create table if not exists auth_permissions (
    id bigserial primary key,
    resource varchar(255) not null,
    action varchar(255) not null,
    created_at timestamp not null default current_timestamp
);

create unique index if not exists idx_auth_permissions_resource_action on auth_permissions (
    resource, action
);

create table if not exists auth_role_permissions (
    role_id bigint not null,
    permission_id bigint not null,
    primary key (role_id, permission_id),
    constraint fk_auth_role_permissions_role_id foreign key (
        role_id
    ) references auth_roles (id) on delete cascade,
    constraint fk_auth_role_permissions_permission_id foreign key (
        permission_id
    ) references auth_permissions (id) on delete cascade
);

create table if not exists auth_user_roles (
    user_id bigint not null,
    role_id bigint not null,
    created_at timestamp not null default current_timestamp,
    expires_at timestamp null,
    primary key (user_id, role_id),
    constraint fk_auth_user_roles_user_id foreign key (
        user_id
    ) references auth_users (id) on delete cascade,
    constraint fk_auth_user_roles_role_id foreign key (
        role_id
    ) references auth_roles (id) on delete cascade
);

create index if not exists idx_auth_user_roles_role_id on auth_user_roles (
    role_id
);
create index if not exists idx_auth_user_roles_expires_at on auth_user_roles (
    expires_at
);
create index if not exists idx_auth_role_permissions_permission_id on auth_role_permissions (
    permission_id
);

create table if not exists auth_api_tokens (
    id bigserial primary key,
    uuid uuid not null unique,
    user_id bigint not null,
    token varchar(255) not null,
    name varchar(100) not null,
    last_used_at timestamp default null,
    expires_at timestamp default null,
    created_at timestamp default current_timestamp,
    updated_at timestamp default current_timestamp,
    deleted_at timestamp default null,
    constraint fk_api_tokens_user foreign key (user_id) references auth_users (
        id
    ) on delete cascade
);

create unique index idx_token on auth_api_tokens (token);
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
