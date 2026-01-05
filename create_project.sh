#!/bin/bash

PROJECT_NAME="go-distributed-task-engine"

echo "Creating Go project: $PROJECT_NAME"

# Create directories
mkdir -p $PROJECT_NAME/cmd/scheduler
mkdir -p $PROJECT_NAME/cmd/worker
mkdir -p $PROJECT_NAME/internal/job
mkdir -p $PROJECT_NAME/internal/queue

# Create files
touch $PROJECT_NAME/cmd/scheduler/main.go
touch $PROJECT_NAME/cmd/worker/main.go
touch $PROJECT_NAME/internal/job/job.go
touch $PROJECT_NAME/internal/job/result.go
touch $PROJECT_NAME/internal/queue/priority_queue.go
touch $PROJECT_NAME/README.md

# Initialize go module
cd $PROJECT_NAME || exit
go mod init github.com/madhavbhayani/$PROJECT_NAME

echo "Go project structure created successfully ✅"
echo "Project path: $(pwd)/$PROJECT_NAME"