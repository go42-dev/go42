-- +goose Up

create table if not exists auth_users (
    id bigint unsigned not null auto_increment primary key,
    uuid char(36) not null,
    password varchar(255) null,
    credential_version bigint not null default 1,
    email varchar(255) not null,
    status varchar(255) not null default 'active',
    is_system boolean not null default false,
    metadata json null,
    created_at timestamp not null default current_timestamp,
    updated_at timestamp not null default current_timestamp,
    deleted_at timestamp null,
    unique key idx_auth_users_uid (uuid),
    unique key idx_auth_users_email (email),
    key idx_auth_users_deleted_at (deleted_at)
) engine = innodb default charset = utf8mb4 collate = utf8mb4_unicode_ci;

create table if not exists auth_sessions (
    id char(36) primary key,
    user_id bigint unsigned not null,
    credential_version bigint not null,
    refresh_token_id char(36) not null,
    expires_at datetime not null,
    revoked_at datetime null,
    created_at datetime not null default current_timestamp,
    updated_at datetime not null default current_timestamp,
    constraint fk_auth_sessions_user foreign key (user_id) references auth_users (
        id
    ) on delete cascade
) engine = innodb default charset = utf8mb4 collate = utf8mb4_unicode_ci;

create index idx_auth_sessions_user_id on auth_sessions (user_id);
create index idx_auth_sessions_expires_at on auth_sessions (expires_at);

create table if not exists auth_users_history
(
    id char(36) primary key,
    occurred_at datetime not null,
    created_at datetime default current_timestamp not null,
    user_id bigint unsigned not null,
    event_type varchar(255) not null,
    data varchar(255) null,
    metadata varchar(1000) null,
    key idx_auth_users_history_occurred_at (occurred_at),
    constraint fk_auth_users_history_user_id foreign key (
        user_id
    ) references auth_users (id) on delete cascade
);

create table if not exists auth_roles (
    id bigint unsigned not null auto_increment primary key,
    name varchar(50) not null,
    description varchar(255) not null default '',
    is_system boolean not null default false,
    created_at timestamp not null default current_timestamp,
    updated_at timestamp not null default current_timestamp on update current_timestamp,
    deleted_at timestamp null,
    unique key idx_auth_roles_name (name),
    key idx_auth_roles_deleted_at (deleted_at)
) engine = innodb default charset = utf8mb4 collate = utf8mb4_unicode_ci;

create table if not exists auth_permissions (
    id bigint unsigned not null auto_increment primary key,
    resource varchar(255) not null,
    action varchar(255) not null,
    created_at timestamp not null default current_timestamp,
    unique key idx_auth_permissions_resource_action (resource, action)
) engine = innodb default charset = utf8mb4 collate = utf8mb4_unicode_ci;

create table if not exists auth_role_permissions (
    role_id bigint unsigned not null,
    permission_id bigint unsigned not null,
    primary key (role_id, permission_id),
    key idx_auth_role_permissions_permission_id (permission_id),
    constraint fk_auth_role_permissions_role_id foreign key (
        role_id
    ) references auth_roles (id) on delete cascade,
    constraint fk_auth_role_permissions_permission_id foreign key (
        permission_id
    ) references auth_permissions (id) on delete cascade
) engine = innodb default charset = utf8mb4 collate = utf8mb4_unicode_ci;

create table if not exists auth_user_roles (
    user_id bigint unsigned not null,
    role_id bigint unsigned not null,
    created_at timestamp not null default current_timestamp,
    expires_at timestamp null default null,
    primary key (user_id, role_id),
    key idx_auth_user_roles_role_id (role_id),
    key idx_auth_user_roles_expires_at (expires_at),
    constraint fk_auth_user_roles_user_id foreign key (
        user_id
    ) references auth_users (id) on delete cascade,
    constraint fk_auth_user_roles_role_id foreign key (
        role_id
    ) references auth_roles (id) on delete cascade
) engine = innodb default charset = utf8mb4 collate = utf8mb4_unicode_ci;

create table if not exists auth_api_tokens (
    id bigint unsigned auto_increment primary key,
    uuid char(36) not null,
    user_id bigint unsigned not null,
    token varchar(255) not null,
    name varchar(100) not null,
    last_used_at timestamp null default null,
    expires_at timestamp null default null,
    created_at timestamp default current_timestamp,
    updated_at timestamp default current_timestamp,
    deleted_at timestamp null default null,
    unique key idx_token (token),
    constraint fk_api_tokens_user foreign key (user_id) references auth_users (
        id
    ) on delete cascade
) engine = innodb default charset = utf8mb4 collate = utf8mb4_unicode_ci;

create index idx_token_lookup on auth_api_tokens (
    token, deleted_at, expires_at
);

create table if not exists auth_api_tokens_permissions (
    token_id bigint unsigned not null,
    permission_id bigint unsigned not null,
    primary key (token_id, permission_id),
    foreign key (token_id) references auth_api_tokens (id) on delete cascade,
    foreign key (permission_id) references auth_permissions (
        id
    ) on delete cascade
);

create index idx_token_permissions_token_id on auth_api_tokens_permissions (
    token_id
);
create index idx_token_permissions_permission_id on auth_api_tokens_permissions (
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
