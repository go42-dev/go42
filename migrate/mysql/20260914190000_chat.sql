-- +goose Up

create table if not exists chat_channels (
    id bigint unsigned not null auto_increment primary key,
    uuid char(36) not null,
    name varchar(255) not null,
    created_by_user_id bigint unsigned not null,
    created_at timestamp not null default current_timestamp,
    updated_at timestamp not null default current_timestamp on update current_timestamp,
    unique key idx_chat_channels_uuid (uuid),
    key idx_chat_channels_created_by (created_by_user_id),
    constraint fk_chat_channels_created_by foreign key (created_by_user_id) references auth_users (id) on delete cascade
) engine = innodb default charset = utf8mb4 collate = utf8mb4_unicode_ci;

create table if not exists chat_messages (
    id bigint unsigned not null auto_increment primary key,
    uuid char(36) not null,
    channel_id bigint unsigned null,
    sender_user_id bigint unsigned not null,
    recipient_user_id bigint unsigned null,
    content text not null,
    created_at timestamp not null default current_timestamp,
    updated_at timestamp not null default current_timestamp on update current_timestamp,
    unique key idx_chat_messages_uuid (uuid),
    key idx_chat_messages_channel_id (channel_id, id),
    key idx_chat_messages_sender_user_id (sender_user_id, id),
    key idx_chat_messages_recipient_user_id (recipient_user_id, id),
    constraint fk_chat_messages_channel foreign key (channel_id) references chat_channels (id) on delete cascade,
    constraint fk_chat_messages_sender foreign key (sender_user_id) references auth_users (id) on delete cascade,
    constraint fk_chat_messages_recipient foreign key (recipient_user_id) references auth_users (id) on delete cascade,
    constraint chk_chat_messages_destination check (
        (channel_id is not null and recipient_user_id is null)
        or (channel_id is null and recipient_user_id is not null)
    )
) engine = innodb default charset = utf8mb4 collate = utf8mb4_unicode_ci;

-- +goose Down

drop table if exists chat_messages;
drop table if exists chat_channels;
