-- Create notifications table
CREATE TABLE IF NOT EXISTS notifications (
    user_id INTEGER PRIMARY KEY,
    use_device_push BOOLEAN NOT NULL DEFAULT FALSE,
    use_sms BOOLEAN NOT NULL DEFAULT FALSE, 
    use_email BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_notifications_user_id 
        FOREIGN KEY (user_id) 
        REFERENCES users(id) 
        ON DELETE CASCADE
);