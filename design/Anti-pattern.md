Anti-pattern?
In software, an anti-pattern is a common but bad way to solve a problem. It might seem like a good idea at first, but it usually leads to poor design, hard-to-maintain code, or hidden bugs. Anti-patterns are misleading solutions that look simple or helpful but cause problems in the long run.

The six anti-patterns I’ll cover in this article are: Spaghetti Code, Golden Hammer, Boat Anchor, Dead Code, Proliferation of Code, and the God Object.

Spaghetti Code
Spaghetti Code is one of the most well-known anti-patterns. It refers to code that has little or no structure, making it messy and hard to follow — like a bowl of tangled spaghetti. This usually happens due to poor planning, unclear goals, or lack of experience. The result is code that’s difficult to read, understand, and maintain.

Golden Hammer
The Golden Hammer is when a team uses the same tool or method to solve every problem, just because they’re used to it. This can lead to poor solutions and stop them from finding better options.

Boat Anchor
A Boat Anchor is when code or a part of a system is kept even though it’s no longer useful. People keep it just in case they need it later — but they almost never do.

God Object
If one object is used everywhere in your code, it might be a God Object. This anti-pattern happens when a single class does too much — it handles many tasks, controls other objects, and becomes the center of everything in the app.

Dead Code
Have you ever seen code written by someone who no longer works at your company? There might be a function that doesn’t seem to do anything, but it’s called everywhere. When you ask others, no one really knows what it does…

God Object / God Class: A single class or object that handles too many responsibilities, violating the Single Responsibility Principle and making code hard to test and maintain.

Magic Numbers and Strings: Hardcoding raw numbers or text directly into logic instead of using named constants, making the code confusing to update.

Copy-Paste (Code Duplication): Repeating identical or very similar logic across multiple places instead of creating a reusable function or module.

Prop Drilling: Passing data down through multiple nested layers of components that do not need the data themselves, commonly seen in UI frameworks like React.

Premature Optimization: Over-engineering or complicating code for performance gains or future needs that are not actually required yet.

Lava Flow: Leaving dead code, experimental blocks, or obsolete structures in place because nobody is sure what breaks if it is removed.