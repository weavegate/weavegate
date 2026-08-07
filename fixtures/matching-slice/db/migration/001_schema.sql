CREATE TABLE project_request (
    id BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL,
    PRIMARY KEY (id)
) ENGINE=InnoDB;

CREATE TABLE matching_session (
    id BIGINT NOT NULL AUTO_INCREMENT,
    status VARCHAR(16) NOT NULL,
    PRIMARY KEY (id)
) ENGINE=InnoDB;

CREATE TABLE assignment (
    id BIGINT NOT NULL AUTO_INCREMENT,
    project_request_id BIGINT NOT NULL,
    matching_session_id BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL,
    PRIMARY KEY (id),
    INDEX idx_assignment_project_request (project_request_id),
    INDEX idx_assignment_matching_session (matching_session_id),
    CONSTRAINT fk_assignment_project_request
        FOREIGN KEY (project_request_id) REFERENCES project_request (id),
    CONSTRAINT fk_assignment_matching_session
        FOREIGN KEY (matching_session_id) REFERENCES matching_session (id)
) ENGINE=InnoDB;
