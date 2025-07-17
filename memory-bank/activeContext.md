# Active Context: workbench-operator

## Current Focus
The immediate focus is on documenting the existing functionality and establishing a baseline understanding of the project. The primary goal is to create a comprehensive "memory bank" that can be used for future development. The memory bank has been updated to reflect the latest changes from the `master` branch.

## Recent Changes
- The application configuration has been switched from a list to a map, providing more flexibility.
- Default resource requests and limits have been implemented for application containers.
- Users can now specify the initial screen resolution for the Xpra server.
- A bug related to nil pointer assignments in maps has been fixed.

## Next Steps
Based on the `README.md`, the following items need to be addressed:
- Handling the lifecycle when the user stops the server from within the workbench.
- Reporting status information for the applications.
- Implementing TLS between the Xpra server and the applications.
- Creating an admission webhook for validation.
- Adding detailed contribution guidelines. 