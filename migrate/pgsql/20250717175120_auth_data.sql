-- +goose Up

insert into auth_users (uuid, password, email, is_system) values
('00000000-0000-0000-0000-000000000000', null, 'admin@system.local', true)
on conflict do nothing;

insert into auth_roles (name, description, is_system) values
('admin', 'full access to entire system', true),
('user', 'standard user role with limited access', false)
on conflict do nothing;

insert into auth_permissions (resource, action) values
('users', 'read_self'),
('users', 'update_self'),
('users', 'list'),
('users', 'read_others'),
('users', 'create'),
('users', 'update'),
('users', 'delete')
on conflict do nothing;

-- admins have all permissions
insert into auth_role_permissions (role_id, permission_id)
select
    (
        select auth_roles.id
        from auth_roles
        where auth_roles.name = 'admin'
    ) as role_id,
    auth_permissions.id as permission_id
from
    auth_permissions
on conflict do nothing;

-- users can read & update themselves
insert into auth_role_permissions (role_id, permission_id) values
(
    (
        select id from auth_roles
        where name = 'user'
    ),
    (
        select id from auth_permissions
        where resource = 'users' and action = 'read_self'
    )
),
(
    (
        select id from auth_roles
        where name = 'user'
    ),
    (
        select id from auth_permissions
        where resource = 'users' and action = 'update_self'
    )
)
on conflict do nothing;

insert into auth_api_tokens (uuid, user_id, token, name)
select
    '00000000-0000-0000-0000-000000000001' as token_uuid,
    auth_users.id as user_id,
    -- api_kXqdf2uQ7hmOARp-pZrhA6_IsZSeKCmSEM4YFKBGIzA
    '67778026319f8a10160230483f9f43a960f3724807ccd04a7c856ade5d09f800' as token,
    'local-development' as token_name
from auth_users
where auth_users.uuid = '00000000-0000-0000-0000-000000000000'
on conflict do nothing;

insert into auth_api_tokens_permissions (token_id, permission_id)
select
    auth_api_tokens.id as token_id,
    auth_permissions.id as permission_id
from auth_api_tokens
cross join auth_permissions
where
    auth_api_tokens.uuid = '00000000-0000-0000-0000-000000000001'
    and auth_permissions.resource = 'users'
    and auth_permissions.action in (
        'list',
        'read_others',
        'create',
        'update',
        'delete'
    )
on conflict do nothing;

-- +goose Down

delete from auth_sessions;
delete from auth_api_tokens_permissions;
delete from auth_api_tokens;
delete from auth_role_permissions;
delete from auth_permissions;
delete from auth_roles;
delete from auth_users;
