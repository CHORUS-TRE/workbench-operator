# Progress: workbench-operator

## What Works
- The operator can successfully create and manage `Workbench` custom resources.
- It correctly sets up the `Deployment` for the Xpra server, a `Service` for network access, and `Jobs` for applications.
- Basic workbench functionality is operational.
- Application configuration is now more flexible with the use of a map.
- Default resource requests and limits are now set for applications, with the ability for users to override them.
- Initial screen resolution can now be configured for the Xpra server.

## What's Left to Build
- Graceful handling of the Xpra server being stopped from within the container.
- Enhanced status reporting for applications.
- Secure communication using TLS.
- An admission webhook for `Workbench` resource validation.
- Comprehensive contribution guidelines.

## Known Issues
- Stopping the Xpra server from within the workbench causes it to restart, which is not an ideal user experience.
- The project lacks detailed contributing guidelines, which could be a barrier for new contributors.
- A previous issue with nil pointer assignments in maps has been fixed. 