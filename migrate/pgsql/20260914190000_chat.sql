-- +goose Up

create table if not exists chat_channels (
    id bigserial primary key,
    uuid uuid not null unique,
    name varchar(255) not null,
    created_by_user_id bigint not null,
    created_at timestamp not null default current_timestamp,
    updated_at timestamp not null default current_timestamp,
    constraint fk_chat_channels_created_by foreign key (created_by_user_id) references auth_users (id) on delete cascade
);

create index if not exists idx_chat_channels_created_by on chat_channels (created_by_user_id);

create table if not exists chat_messages (
    id bigserial primary key,
    uuid uuid not null unique,
    channel_id bigint null,
    sender_user_id bigint not null,
    recipient_user_id bigint null,
    content text not null,
    created_at timestamp not null default current_timestamp,
    updated_at timestamp not null default current_timestamp,
    constraint fk_chat_messages_channel foreign key (channel_id) references chat_channels (id) on delete cascade,
    constraint fk_chat_messages_sender foreign key (sender_user_id) references auth_users (id) on delete cascade,
    constraint fk_chat_messages_recipient foreign key (recipient_user_id) references auth_users (id) on delete cascade,
    constraint chk_chat_messages_destination check (
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
