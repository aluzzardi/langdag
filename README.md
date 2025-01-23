# LangDAG

Use [dagger](https://dagger.io) modules as LLM tools.

By writing tools as modules, you get all the benefits dagger provides:

* Write in any language, use from any language
* Sandboxing via containerization
* Caching

## Examples

* [simple](./examples/simple/): OpenAI boilerplate providing a module as a set of tools
* [ChatMOD](./examples/chatmod/): Chat with a dagger module.
* [Agent](./examples/agent/): Use modules to accomplish a goal, reacting to GitHub webhooks.

## Modules

* [trufflehog](./modules/trufflehog/)
* [GitHub](./modules/github/)
