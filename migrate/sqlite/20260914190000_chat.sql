-- +goose Up

create table if not exists chat_channels (
    id integer primary key autoincrement,
    uuid text not null unique,
    name text not null,
    created_by_user_id integer not null,
    created_at datetime not null default current_timestamp,
    updated_at datetime not null default current_timestamp,
    foreign key (created_by_user_id) references auth_users (id) on delete cascade
);

create index if not exists idx_chat_channels_created_by on chat_channels (created_by_user_id);

create table if not exists chat_messages (
    id integer primary key autoincrement,
    uuid text not null unique,
    channel_id integer,
    sender_user_id integer not null,
    recipient_user_id integer,
    content text not null,
    created_at datetime not null default current_timestamp,
    updated_at datetime not null default current_timestamp,
    foreign key (channel_id) references chat_channels (id) on delete cascade,
    foreign key (sender_user_id) references auth_users (id) on delete cascade,
    foreign key (recipient_user_id) references auth_users (id) on delete cascade,
    check (
        (channel_id is not null and recipient_user_id is null)
        or (channel_id is null and recipient_user_id is not null)
    )
);

create index if not exists idx_chat_messages_channel_id on chat_messages (channel_id, id);
create index if not exists idx_chat_messages_sender_user_id on chat_messages (sender_user_id, id);
create index if not exists idx_chat_messages_recipient_user_id on chat_messages (recipient_user_id, id);

-- +goose Down

drop table if exists chat_messages;
drop table if exists chat_channels;
